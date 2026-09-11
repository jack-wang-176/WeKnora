<script setup lang="ts">
import { computed } from 'vue'
import { getDatasourceIconUrl, datasourceIconMap } from './datasourceIcons'

const props = withDefaults(defineProps<{
  type: string
  size?: number
  /** inline: 类型选择等小尺寸场景；badge: 嵌入 ds-card__badge 等父级徽章容器 */
  variant?: 'inline' | 'badge'
  /** 后端 metadata 里的图标，内置图标表没有该类型时才用，URL 或 emoji 均可 */
  src?: string
}>(), {
  size: 20,
  variant: 'inline',
  src: '',
})

const remoteSrc = computed(() => (/^(https?:|data:)/.test(props.src) ? props.src : ''))
const glyph = computed(() => (props.src && !remoteSrc.value ? props.src : ''))

const iconMap = datasourceIconMap

function fallbackText(type: string) {
  switch (type) {
    case 'feishu':
      return 'F'
    case 'lark':
      return 'L'
    case 'notion':
      return 'N'
    case 'yuque':
      return 'Y'
    case 'ima':
      return 'I'
    default:
      return type.slice(0, 1).toUpperCase() || '?'
  }
}
</script>

<template>
  <span
    class="ds-type-icon"
    :class="`ds-type-icon--${variant}`"
    :style="variant === 'inline' ? { width: `${size}px`, height: `${size}px` } : undefined"
  >
    <img
      v-if="iconMap[type] || remoteSrc"
      :src="iconMap[type] || remoteSrc"
      :alt="type"
      class="ds-type-icon__img"
      :style="variant === 'inline' ? { width: `${size}px`, height: `${size}px` } : undefined"
    >
    <span v-else class="ds-type-icon-fallback">{{ glyph || fallbackText(type) }}</span>
  </span>
</template>

<style scoped>
.ds-type-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  overflow: hidden;
}

.ds-type-icon--inline {
  border-radius: 6px;
  background: var(--td-bg-color-component);
}

.ds-type-icon--inline .ds-type-icon__img {
  display: block;
  object-fit: contain;
}

.ds-type-icon--inline .ds-type-icon-fallback {
  font-size: 11px;
  font-weight: 600;
  color: var(--td-text-color-placeholder);
}

.ds-type-icon--badge {
  width: 100%;
  height: 100%;
  background: transparent;
}

.ds-type-icon--badge .ds-type-icon__img {
  display: block;
  width: 24px;
  height: 24px;
  object-fit: contain;
}

.ds-type-icon--badge .ds-type-icon-fallback {
  font-size: 15px;
  font-weight: 600;
  letter-spacing: 0.02em;
  color: inherit;
}
</style>
