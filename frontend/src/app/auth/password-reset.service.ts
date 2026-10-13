import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';

import { AuthService } from './auth.service';

const RESET_API = 'api/auth/password';

/** Password reset by email (public endpoints, no session needed). */
@Injectable({ providedIn: 'root' })
export class PasswordResetService {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);

  /** Always resolves for a valid email, whether or not an account exists. */
  async forgot(email: string): Promise<void> {
    await this.ensureCsrfCookie();
    await firstValueFrom(this.http.post<void>(`${RESET_API}/forgot`, { email }));
  }

  /** Sets the new password; the backend revokes every session of the user. */
  async reset(token: string, password: string): Promise<void> {
    await this.ensureCsrfCookie();
    await firstValueFrom(this.http.post<void>(`${RESET_API}/reset`, { token, password }));
    this.auth.markLoggedOut();
  }

  /**
   * The /api CSRF check needs the XSRF-TOKEN cookie, which the backend sets on
   * any GET. loadUser (GET /api/auth/me, cached) guarantees one has happened.
   */
  private async ensureCsrfCookie(): Promise<void> {
    await this.auth.loadUser();
  }
}
