import { useEffect, useState } from "react";
import type { Card } from "../../shared/protocol";
import { cardLabel, isRed } from "./cards";
import { faceFor } from "./cardFaces";

export function PlayingCard({ card, compact = false, flipped = false }: { card: Card; compact?: boolean; flipped?: boolean }) {
  const Face = faceFor(card);
  return (
    <div className={`playing-card asset-card ${compact ? "compact" : ""} ${isRed(card) ? "red" : "black"} ${flipped ? "flip-in" : ""}`} aria-label={cardLabel(card)}>
      <Face title={cardLabel(card)} width="100%" height="100%" />
    </div>
  );
}

export function CardBack({ compact = false }: { compact?: boolean }) {
  return <div className={`card-back ${compact ? "compact" : ""}`} aria-label="Face-down card" />;
}

export function PeekableCard({ card, compact = false }: { card?: Card; compact?: boolean }) {
  const [face, setFace] = useState<Card | undefined>(card);
  const [revealed, setRevealed] = useState(false);

  useEffect(() => {
    let firstFrame: number | undefined;
    let secondFrame: number | undefined;
    let hideTimer: number | undefined;
    if (card) {
      setFace(card);
      setRevealed(false);
      firstFrame = window.requestAnimationFrame(() => {
        secondFrame = window.requestAnimationFrame(() => setRevealed(true));
      });
    } else if (face) {
      setRevealed(false);
      hideTimer = window.setTimeout(() => setFace(undefined), 300);
    }
    return () => {
      if (firstFrame !== undefined) window.cancelAnimationFrame(firstFrame);
      if (secondFrame !== undefined) window.cancelAnimationFrame(secondFrame);
      if (hideTimer !== undefined) window.clearTimeout(hideTimer);
    };
  }, [card?.id]);

  if (!face) return <CardBack compact={compact} />;
  return (
    <div className={`peek-card ${compact ? "compact" : ""} ${revealed ? "revealed" : ""}`} aria-label={revealed ? cardLabel(face) : "Face-down card"}>
      <div className="peek-card-layer peek-card-back" aria-hidden="true"><CardBack compact={compact} /></div>
      <div className="peek-card-layer peek-card-front" aria-hidden="true"><PlayingCard card={face} compact={compact} /></div>
    </div>
  );
}
