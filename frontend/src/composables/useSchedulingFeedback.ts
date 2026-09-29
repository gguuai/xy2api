import { onMounted, onUnmounted, type Ref } from 'vue'
import { gsap } from 'gsap'

/** A short confirmation, never an entrance delay on the account table. */
export function useSchedulingFeedback(root: Ref<HTMLElement | null>) {
  let media: gsap.MatchMedia | undefined
  let animate: ((element: HTMLElement) => void) | undefined
  onMounted(() => {
    media = gsap.matchMedia()
    media.add('(prefers-reduced-motion: no-preference)', context => {
      animate = element => {
        context.add(() => {
          gsap.fromTo(element, { opacity: 0.55, y: 3 }, {
            opacity: 1, y: 0, duration: 0.2, ease: 'power2.out', overwrite: true,
            clearProps: 'opacity,transform'
          })
        })
      }
      return () => { animate = undefined }
    }, root.value ?? undefined)
  })
  onUnmounted(() => { animate = undefined; media?.revert() })
  return (element: HTMLElement | null) => { if (element?.isConnected) animate?.(element) }
}
