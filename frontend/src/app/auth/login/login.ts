import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { NonNullableFormBuilder, ReactiveFormsModule, Validators } from '@angular/forms';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { TranslocoPipe } from '@jsverse/transloco';

import { AuthService, safeReturnUrl } from '../auth.service';
import { OAuthButtons } from '../oauth/oauth-buttons';
import { MessagePipe, type Message } from '../../i18n/message';
import { applyServerErrors, controlError } from '../form-errors';

@Component({
  selector: 'app-login',
  imports: [ReactiveFormsModule, RouterLink, OAuthButtons, TranslocoPipe, MessagePipe],
  templateUrl: './login.html',
  styleUrl: '../auth-form.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Login {
  private readonly auth = inject(AuthService);
  private readonly router = inject(Router);
  private readonly route = inject(ActivatedRoute);

  readonly form = inject(NonNullableFormBuilder).group({
    username: ['', [Validators.required, Validators.maxLength(30)]],
    password: ['', [Validators.required, Validators.maxLength(128)]],
  });
  readonly submitting = signal(false);
  readonly formError = signal<Message | null>(null);

  readonly controlError = controlError;

  async submit(): Promise<void> {
    if (this.form.invalid || this.submitting()) {
      this.form.markAllAsTouched();
      return;
    }
    this.submitting.set(true);
    this.formError.set(null);
    const { username, password } = this.form.getRawValue();
    try {
      await this.auth.login(username.trim(), password);
      const returnUrl = safeReturnUrl(this.route.snapshot.queryParamMap.get('returnUrl'));
      await this.router.navigateByUrl(returnUrl);
    } catch (error) {
      this.formError.set(applyServerErrors(this.form, error, 'errors.loginFailed'));
    } finally {
      this.submitting.set(false);
    }
  }
}
