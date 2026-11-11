// The login session (docs/api.md → Auth). In localhost mode it's always "authenticated"
// and has no CSRF token; in LAN mode the app shows the login form until it is.
import { getSession, login as apiLogin, logout as apiLogout, setCsrfToken, setUnauthorizedHandler, type Session } from './api';

export const auth = $state<{ session: Session | null; needLogin: boolean; error: string }>({
  session: null,
  needLogin: false,
  error: '',
});

function apply(s: Session): void {
  auth.session = s;
  auth.error = '';
  setCsrfToken(s.csrf_token);
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
  apply(await apiLogin(password));
}

export async function logout(): Promise<void> {
  try {
    await apiLogout();
  } finally {
    await loadSession();
  }
}

// Any 401 means the session ended (expired, logged out elsewhere, password changed):
// show the login form in place. The URL is untouched, so after login the user is back
// on the same page.
setUnauthorizedHandler(() => {
  setCsrfToken('');
  if (auth.session) auth.session = { ...auth.session, authenticated: false, csrf_token: '' };
  auth.needLogin = true;
});
