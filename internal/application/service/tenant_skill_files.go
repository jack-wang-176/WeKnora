package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

type SkillFileEntry = BundleFileEntry
type SkillFileContent = BundleFileContent

// ListSkillFiles lists the stored archive of one installed skill. The files
// come from the uploaded bundle rather than the live image: browsing must
// work while the skill is still installing, and without booting a sandbox.
func (s *TenantSkillService) ListSkillFiles(
	ctx context.Context, tenantID uint64, configID, skillID string,
) ([]SkillFileEntry, error) {
	archive, err := s.skillBundleArchive(ctx, tenantID, configID, skillID)
	if err != nil {
		return nil, err
	}
	return listBundleZipFiles(archive)
}

// ReadSkillFile returns one file from the stored archive. Binary files are
// either inlined as base64 (images small enough to preview) or reported
// without a body so the UI can say they cannot be opened.
func (s *TenantSkillService) ReadSkillFile(
	ctx context.Context, tenantID uint64, configID, skillID, relativePath string,
) (*SkillFileContent, error) {
	clean, err := safeBundleFilePath(relativePath)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	archive, err := s.skillBundleArchive(ctx, tenantID, configID, skillID)
	if err != nil {
		return nil, err
	}
	body, err := readBundleZipFile(archive, clean)
	if err != nil {
		if errors.Is(err, errBundleFileMissing) {
			return nil, apperrors.NewNotFoundError("skill file not found")
		}
		return nil, err
	}
	return projectBundleFileContent(clean, body), nil
}

func (s *TenantSkillService) skillBundleArchive(
	ctx context.Context, tenantID uint64, configID, skillID string,
) ([]byte, error) {
	skill, err := s.skills.GetSkill(ctx, tenantID, configID, skillID)
	if err != nil {
		return nil, err
	}
	if skill == nil {
		return nil, apperrors.NewNotFoundError("skill not found")
	}
	// An object named by the row itself is the archive this sandbox was built
	// from, so it answers first and needs no digest check.
	if archive, ok := s.trySkillBundle(ctx, tenantID, skill); ok {
		return archive, nil
	}
	// Otherwise the definition holds the zip. It answers for this install only
	// while the digests agree: registering the skill again replaces the catalog
	// object in place, while every sandbox keeps running the image built from
	// the archive its row names. Serving the newer bytes would show the admin
	// (and read_skill) a tree that image does not have.
	if archive, err := s.sameDigestCatalogArchive(ctx, tenantID, skill); err == nil && len(archive) > 0 {
		return archive, nil
	}
	if strings.TrimSpace(skill.BundleSHA256) == "" {
		// A row that recorded no digest predates the catalog and has nothing to
		// check against, so the definition's copy is the only answer available.
		if archive, err := s.anyCatalogArchiveFor(ctx, tenantID, skill); err == nil && len(archive) > 0 {
			return archive, nil
		}
	}
	return nil, apperrors.NewNotFoundError("skill files are not available")
}

// anyCatalogArchiveFor resolves the definition an install belongs to without
// checking what it holds. It exists for rows written before the catalog, which
// carry neither a bundle reference nor a digest.
func (s *TenantSkillService) anyCatalogArchiveFor(
	ctx context.Context, tenantID uint64, skill *types.TenantSkillEntity,
) ([]byte, error) {
	if cid := strings.TrimSpace(skill.CatalogID); cid != "" {
		if archive, err := s.loadCatalogArchive(ctx, tenantID, cid); err == nil && len(archive) > 0 {
			return archive, nil
		}
	}
	return s.loadCatalogArchive(ctx, tenantID, skill.ID)
}

func (s *TenantSkillService) sameDigestCatalogArchive(
	ctx context.Context, tenantID uint64, skill *types.TenantSkillEntity,
) ([]byte, error) {
	if skill == nil || strings.TrimSpace(skill.BundleSHA256) == "" {
		return nil, apperrors.NewNotFoundError("skill files are not available")
	}
	cid := strings.TrimSpace(skill.CatalogID)
	if cid == "" {
		cid = skill.ID
	}
	archive, err := s.loadCatalogArchive(ctx, tenantID, cid)
	if err != nil {
		return nil, err
	}
	if !archiveMatchesSHA(archive, skill.BundleSHA256) {
		return nil, apperrors.NewNotFoundError("skill files are not available")
	}
	return archive, nil
}

func (s *TenantSkillService) trySkillBundle(
	ctx context.Context, tenantID uint64, skill *types.TenantSkillEntity,
) ([]byte, bool) {
	if skill == nil || strings.TrimSpace(skill.BundleRef) == "" {
		return nil, false
	}
	key := bundleCacheKey("skill", tenantID, skillCacheID(skill))
	if cached := s.bundleCache.get(key); cached != nil {
		return cached, true
	}
	v, err, _ := s.bundleLoad.Do(key, func() (interface{}, error) {
		if cached := s.bundleCache.get(key); cached != nil {
			return cached, nil
		}
		archive, err := s.downloadSkillBundle(ctx, tenantID, skill)
		if err != nil {
			return nil, err
		}
		s.bundleCache.put(key, archive)
		return archive, nil
	})
	if err != nil {
		return nil, false
	}
	archive, ok := v.([]byte)
	if !ok || len(archive) == 0 {
		return nil, false
	}
	return archive, true
}

func skillCacheID(skill *types.TenantSkillEntity) string {
	if id := strings.TrimSpace(skill.BundleSHA256); id != "" {
		return id
	}
	return strings.TrimSpace(skill.BundleRef)
}

func (s *TenantSkillService) downloadSkillBundle(
	ctx context.Context, tenantID uint64, skill *types.TenantSkillEntity,
) ([]byte, error) {
	ref := strings.TrimSpace(skill.BundleRef)
	fs, err := s.fileServiceForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	reader, err := fs.GetFile(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("download bundle of skill %s: %w", skill.Name, err)
	}
	defer func() { _ = reader.Close() }()
	archive, err := io.ReadAll(io.LimitReader(reader, maxSkillBundleTotalBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read bundle of skill %s: %w", skill.Name, err)
	}
	if len(archive) > maxSkillBundleTotalBytes {
		return nil, fmt.Errorf("skill bundle %s is larger than the upload limit", ref)
	}
	return archive, nil
}
