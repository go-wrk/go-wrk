import { onBeforeUnmount, onMounted, ref, type Ref } from "vue"

/**
 * 跟踪元素的实际像素宽度。
 *
 * 图表用真实像素宽度当坐标系宽度，而不是靠 SVG 的 viewBox 缩放 ——
 * 缩放会把线宽和字号一起拉变形，字会糊。
 */
export function useElementWidth(): {
  el: Ref<HTMLElement | undefined>
  width: Ref<number>
} {
  const el = ref<HTMLElement>()
  const width = ref(600)

  let observer: ResizeObserver | undefined

  onMounted(() => {
    if (!el.value) return
    observer = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width ?? 0
      if (w > 0) width.value = w
    })
    observer.observe(el.value)
  })

  onBeforeUnmount(() => observer?.disconnect())

  return { el, width }
}
