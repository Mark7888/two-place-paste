import { useId, useRef, useState, type FormEvent } from "react";
import { messageOf } from "../api";
import { ErrorNotice } from "../components/Notice";
import { PasswordInput } from "../components/PasswordInput";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { useSession } from "../session";

/** LoginPage is the sign-in screen. Signing in is all it does: the app sees
 *  the session change and moves on to the dashboard. */
export function LoginPage() {
  useDocumentTitle("Sign in");
  const { signIn } = useSession();
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const id = useId();
  const input = useRef<HTMLInputElement>(null);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await signIn(password);
    } catch (err) {
      // A refused password starts over with an empty field, ready to type.
      setError(messageOf(err));
      setPassword("");
      setBusy(false);
      input.current?.focus();
    }
  };

  return (
    <>
      <ErrorNotice error={error} />
      <div className="auth">
        <h1>Sign in</h1>
        <p className="lede muted">The admin password is set in the relay&rsquo;s configuration.</p>

        <form onSubmit={submit}>
          <div className="field">
            <label htmlFor={id}>Admin password</label>
            <PasswordInput
              ref={input}
              id={id}
              name="password"
              autoComplete="current-password"
              autoFocus
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          <button type="submit" disabled={busy}>
            Sign in
          </button>
        </form>
      </div>
    </>
  );
}
