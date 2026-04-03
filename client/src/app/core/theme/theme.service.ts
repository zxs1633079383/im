import { Injectable, signal, effect } from '@angular/core';

export type Theme = 'system' | 'light' | 'dark';

const STORAGE_KEY = 'app_theme';

@Injectable({ providedIn: 'root' })
export class ThemeService {
  theme = signal<Theme>((localStorage.getItem(STORAGE_KEY) as Theme) || 'dark');

  private mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
  private mediaListener: ((e: MediaQueryListEvent) => void) | null = null;

  constructor() {
    effect(() => {
      const t = this.theme();
      localStorage.setItem(STORAGE_KEY, t);
    });
  }

  applyTheme(theme: Theme): void {
    this.theme.set(theme);

    // Remove old media listener if any
    if (this.mediaListener) {
      this.mediaQuery.removeEventListener('change', this.mediaListener);
      this.mediaListener = null;
    }

    if (theme === 'system') {
      const apply = (dark: boolean) => {
        document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
      };
      apply(this.mediaQuery.matches);
      this.mediaListener = (e: MediaQueryListEvent) => apply(e.matches);
      this.mediaQuery.addEventListener('change', this.mediaListener);
    } else {
      document.documentElement.setAttribute('data-theme', theme);
    }
  }
}
