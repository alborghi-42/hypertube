import { isPlatformBrowser } from '@angular/common';
import { Component, PLATFORM_ID, computed, inject } from '@angular/core';
import { Router, RouterLink } from '@angular/router';

import { AuthService } from '../auth/auth.service';
import { LANGUAGES, LanguageService } from './language.service';

@Component({
  selector: 'app-header',
  imports: [RouterLink],
  templateUrl: './app-header.html',
  styleUrl: './app-header.scss',
})
export class AppHeader {
  private readonly auth = inject(AuthService);
  private readonly router = inject(Router);
  protected readonly language = inject(LanguageService);

  protected readonly languages = LANGUAGES;
  protected readonly user = this.auth.user;
  protected readonly initials = computed(() => {
    const user = this.user();
    if (!user) {
      return '';
    }
    const fromName = `${user.first_name.charAt(0)}${user.last_name.charAt(0)}`;
    return (fromName || user.username.slice(0, 2)).toUpperCase();
  });

  constructor() {
    // Guard-free pages (e.g. /reset-password) never load the session: do it here.
    if (isPlatformBrowser(inject(PLATFORM_ID))) {
      void this.auth.loadUser();
    }
  }

  protected onLanguageChange(event: Event): void {
    this.language.set((event.target as HTMLSelectElement).value);
  }

  protected async logout(): Promise<void> {
    try {
      await this.auth.logout();
    } finally {
      await this.router.navigateByUrl('/login');
    }
  }
}
