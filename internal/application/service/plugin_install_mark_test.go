package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type installMarkRepository struct {
	repository.TenantPluginRepository
	row *types.TenantPlugin
}

func (repo *installMarkRepository) GetPlugin(context.Context, *uint64, string) (*types.TenantPlugin, error) {
	return repo.row, nil
}

func TestPluginInstallMarkSurvivesPostgresPrecision(t *testing.T) {
	now := time.Date(2026, 9, 12, 8, 0, 0, 123456789, time.UTC)
	store := &installMarkRepository{}
	service := &TenantPluginService{plugins: store, now: func() time.Time { return now }}
	stamp := service.timestamp()
	require.Equal(t, 0, stamp.Nanosecond()%1000)
	storedStamp := stamp.Truncate(time.Microsecond)
	row := &types.TenantPlugin{ID: "row", PluginID: "source--10000", Status: types.PluginStatusInstalling, InstallingSince: &stamp}
	stored := *row
	stored.InstallingSince = &storedStamp
	store.row = &stored
	mark := &installMark{}
	mark.set(stamp)
	_, owned := service.stillOwns(t.Context(), row, mark)
	require.True(t, owned)
	later := storedStamp.Add(time.Microsecond)
	stored.InstallingSince = &later
	_, owned = service.stillOwns(t.Context(), row, mark)
	require.False(t, owned)
}
