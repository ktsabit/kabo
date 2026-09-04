import { useEffect } from "react";

export function useVisualViewport(): void {
  useEffect(() => {
    const viewport = window.visualViewport;
    let frame: number | undefined;
    const write = () => {
      frame = undefined;
      const height = viewport?.height ?? window.innerHeight;
      const top = viewport?.offsetTop ?? 0;
      document.documentElement.style.setProperty("--app-height", `${Math.round(height)}px`);
      document.documentElement.style.setProperty("--app-offset-top", `${Math.round(top)}px`);
    };
    const schedule = () => {
      if (frame !== undefined) return;
      frame = window.requestAnimationFrame(write);
    };
    write();
    window.addEventListener("resize", schedule, { passive: true });
    viewport?.addEventListener("resize", schedule, { passive: true });
    viewport?.addEventListener("scroll", schedule, { passive: true });
    return () => {
      window.removeEventListener("resize", schedule);
      viewport?.removeEventListener("resize", schedule);
      viewport?.removeEventListener("scroll", schedule);
      if (frame !== undefined) window.cancelAnimationFrame(frame);
    };
  }, []);
}
