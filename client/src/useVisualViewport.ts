import { useEffect } from "react";

export function useVisualViewport(): void {
  useEffect(() => {
    const viewport = window.visualViewport;
    let frame: number | undefined;
    const write = () => {
      frame = undefined;
      const height = viewport?.height ?? window.innerHeight;
      const width = viewport?.width ?? window.innerWidth;
      const top = viewport?.offsetTop ?? 0;
      document.documentElement.style.setProperty("--app-height", `${Math.round(height)}px`);
      document.documentElement.style.setProperty("--app-width", `${Math.round(width)}px`);
      document.documentElement.style.setProperty("--app-offset-top", `${Math.round(top)}px`);
      document.documentElement.classList.toggle("viewport-compact", width <= 899);
      document.documentElement.classList.toggle("viewport-narrow", width <= 560);
      document.documentElement.classList.toggle("viewport-short", height <= 430 || (width <= 560 && height <= 500));
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
      document.documentElement.classList.remove("viewport-compact", "viewport-narrow", "viewport-short");
    };
  }, []);
}
