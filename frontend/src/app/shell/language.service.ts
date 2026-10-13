import { isPlatformBrowser } from '@angular/common';
import { Injectable, PLATFORM_ID, inject, signal } from '@angular/core';

export interface Language {
  code: string;
  label: string;
}

export const LANGUAGES: readonly Language[] = [
  { code: 'en', label: 'English' },
  { code: 'it', label: 'Italiano' },
  { code: 'fr', label: 'Français' },
];

const STORAGE_KEY = 'ht-language';
const DEFAULT_LANGUAGE = 'en';

function isKnown(code: string | null): code is string {
  return LANGUAGES.some((lang) => lang.code === code);
}

/**
 * UI language choice. Placeholder until i18n (42-9): it only remembers the pick.
 * localStorage is touched in the browser only, so SSR always renders the default.
 */
@Injectable({ providedIn: 'root' })
export class LanguageService {
  private readonly isBrowser = isPlatformBrowser(inject(PLATFORM_ID));
  private readonly state = signal(this.readStored());

  readonly current = this.state.asReadonly();

  set(code: string): void {
    if (!isKnown(code)) {
      return;
    }
    this.state.set(code);
    if (this.isBrowser) {
      try {
        localStorage.setItem(STORAGE_KEY, code);
      } catch {
        // Storage can be unavailable (private mode, quota); the in-memory choice still applies.
      }
    }
  }

  private readStored(): string {
    if (!this.isBrowser) {
      return DEFAULT_LANGUAGE;
    }
    try {
      const stored = localStorage.getItem(STORAGE_KEY);
      return isKnown(stored) ? stored : DEFAULT_LANGUAGE;
    } catch {
      return DEFAULT_LANGUAGE;
    }
  }
}
