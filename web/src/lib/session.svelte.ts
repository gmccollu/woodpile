// The login session (docs/api.md → Auth). In localhost mode (without require_login_local)
// it's always "authenticated"; otherwise the app shows the login form until it is. The
// session secret from login is kept in localStorage by api.ts and sent on every request.
import { getSession, login as apiLogin, logout as apiLogout, setSessionSecret, setUnauthorizedHandler, type Session } from './api';

export const auth = $state<{ session: Session | null; needLogin: boolean; error: string; movedTo: string }>({
  session: null,
  needLogin: false,
  error: '',
  /** Set after a lan_mode or port change: the server restarted at this address, so this page is stale. */
  movedTo: '',
});

function apply(s: Session): void {
  auth.session = s;
  auth.error = '';
  auth.needLogin = s.auth_required && !s.authenticated;
}

export async function loadSession(): Promise<void> {
  try {
    apply(await getSession());
  } catch (e) {
    auth.error = (e as Error).message;
  }
}

/** Throws ApiError (401 wrong password, 429 with retryAfter) for the login form to show. */
export async function login(password: string): Promise<void> {
  const r = await apiLogin(password);
  setSessionSecret(r.session_secret);
  // Never keep the secret in app state: only the session flags.
  apply({ mode: r.mode, auth_required: r.auth_required, authenticated: r.authenticated, password_set: r.password_set });
}

export async function logout(): Promise<void> {
  try {
    await apiLogout();
  } finally {
    setSessionSecret('');
    await loadSession();
  }
}

// Any 401 means the session ended (expired, logged out elsewhere, password changed):
// show the login form in place. The URL is untouched, so after login the user is back
// on the same page.
setUnauthorizedHandler(() => {
  // After a move, 401s come from the old origin; keep the "go to the new address" notice up.
  if (auth.movedTo) return;
  // api.ts has already cleared the stored secret.
  if (auth.session) auth.session = { ...auth.session, authenticated: false };
  auth.needLogin = true;
});
