import { useCopyFeedback, type CopyFeedback } from "../hooks/useCopyFeedback";

/** CopyButton copies a string and says "Copied" on itself for a moment. Pass
 *  feedback to share its state with something else on screen, such as the
 *  dialog's status line. */
export function CopyButton({
  text,
  label,
  className = "secondary",
  feedback,
}: {
  text: string;
  label: string;
  className?: string;
  feedback?: CopyFeedback;
}) {
  const own = useCopyFeedback();
  const fb = feedback ?? own;
  return (
    <button type="button" className={className} onClick={() => void fb.copy(text)}>
      <span>{fb.message ?? label}</span>
    </button>
  );
}
