// Light/dark mode: follows the OS until the user picks one, then remembers it.

const KEY = 'woodpile.theme';
type Mode = 'light' | 'dark';

const media = matchMedia('(prefers-color-scheme: dark)');

function stored(): Mode | null {
  try {
    const v = localStorage.getItem(KEY);
    return v === 'light' || v === 'dark' ? v : null;
  } catch {
    return null;
  }
}

export const theme = $state<{ mode: Mode }>({ mode: stored() ?? (media.matches ? 'dark' : 'light') });

function apply(): void {
  document.documentElement.classList.toggle('dark', theme.mode === 'dark');
}

export function setTheme(mode: Mode): void {
  theme.mode = mode;
  try {
    localStorage.setItem(KEY, mode);
  } catch {
    /* private mode: still switch, just don't remember */
  }
  apply();
}

media.addEventListener('change', (e) => {
  if (stored()) return;
  theme.mode = e.matches ? 'dark' : 'light';
  apply();
});

apply();
