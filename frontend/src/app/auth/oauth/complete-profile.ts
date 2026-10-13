import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { isPlatformBrowser } from '@angular/common';
import {
  ChangeDetectionStrategy,
  Component,
  type OnInit,
  PLATFORM_ID,
  inject,
  signal,
} from '@angular/core';
import { NonNullableFormBuilder, ReactiveFormsModule } from '@angular/forms';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';

import { type AuthErrorBody, type AuthUser, AuthService, safeReturnUrl } from '../auth.service';
import { applyServerErrors, controlError } from '../form-errors';
import { NAME_VALIDATORS, USERNAME_VALIDATORS } from '../validation';

/** Relative to <base href>. */
const SIGNUP_API = 'api/auth/oauth/signup';

interface PendingSignup {
  provider: string;
  email: string;
  username: string;
  first_name: string;
  last_name: string;
}

const PROVIDER_LABELS: Record<string, string> = { '42': '42', github: 'GitHub' };

/**
 * First OAuth login with no matching account: the backend holds the verified provider
 * identity (httpOnly cookie) and this page collects the missing/conflicting fields.
 */
@Component({
  selector: 'app-complete-profile',
  imports: [ReactiveFormsModule, RouterLink],
  templateUrl: './complete-profile.html',
  styleUrl: '../auth-form.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CompleteProfile implements OnInit {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly router = inject(Router);
  private readonly isBrowser = isPlatformBrowser(inject(PLATFORM_ID));

  readonly form = inject(NonNullableFormBuilder).group({
    username: ['', USERNAME_VALIDATORS],
    first_name: ['', NAME_VALIDATORS],
    last_name: ['', NAME_VALIDATORS],
  });
  readonly pending = signal<PendingSignup | null>(null);
  readonly loading = signal(true);
  readonly submitting = signal(false);
  readonly formError = signal<string | null>(null);

  readonly controlError = controlError;
  readonly providerLabel = (p: string): string => PROVIDER_LABELS[p] ?? p;

  async ngOnInit(): Promise<void> {
    if (!this.isBrowser) {
      return;
    }
    try {
      const pending = await firstValueFrom(this.http.get<PendingSignup>(SIGNUP_API));
      this.pending.set(pending);
      this.form.patchValue(pending);
      this.form.markAllAsTouched();
    } catch (error) {
      const body = (
        error instanceof HttpErrorResponse ? error.error : null
      ) as AuthErrorBody | null;
      this.formError.set(body?.error ?? 'No pending sign-up, please log in again');
    } finally {
      this.loading.set(false);
    }
  }

  async submit(): Promise<void> {
    if (this.form.invalid || this.submitting()) {
      this.form.markAllAsTouched();
      return;
    }
    this.submitting.set(true);
    this.formError.set(null);
    const value = this.form.getRawValue();
    try {
      const res = await firstValueFrom(
        this.http.post<{ user: AuthUser; return_url: string }>(SIGNUP_API, {
          username: value.username.trim(),
          first_name: value.first_name.trim(),
          last_name: value.last_name.trim(),
        }),
      );
      this.auth.adopt(res.user);
      await this.router.navigateByUrl(safeReturnUrl(res.return_url));
    } catch (error) {
      this.formError.set(applyServerErrors(this.form, error, 'Sign-up failed, please try again'));
    } finally {
      this.submitting.set(false);
    }
  }
}
