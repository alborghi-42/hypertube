import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { NonNullableFormBuilder, ReactiveFormsModule } from '@angular/forms';
import { Router, RouterLink } from '@angular/router';
import { TranslocoPipe } from '@jsverse/transloco';

import { AuthService } from '../auth.service';
import { OAuthButtons } from '../oauth/oauth-buttons';
import { MessagePipe, type Message } from '../../i18n/message';
import { applyServerErrors, controlError } from '../form-errors';
import {
  EMAIL_VALIDATORS,
  NAME_VALIDATORS,
  PASSWORD_VALIDATORS,
  USERNAME_VALIDATORS,
} from '../validation';

@Component({
  selector: 'app-register',
  imports: [ReactiveFormsModule, RouterLink, OAuthButtons, TranslocoPipe, MessagePipe],
  templateUrl: './register.html',
  styleUrl: '../auth-form.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Register {
  private readonly auth = inject(AuthService);
  private readonly router = inject(Router);

  readonly form = inject(NonNullableFormBuilder).group({
    email: ['', EMAIL_VALIDATORS],
    username: ['', USERNAME_VALIDATORS],
    first_name: ['', NAME_VALIDATORS],
    last_name: ['', NAME_VALIDATORS],
    password: ['', PASSWORD_VALIDATORS],
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
    const value = this.form.getRawValue();
    try {
      await this.auth.register({
        email: value.email.trim(),
        username: value.username.trim(),
        first_name: value.first_name.trim(),
        last_name: value.last_name.trim(),
        password: value.password,
      });
      await this.router.navigateByUrl('/');
    } catch (error) {
      this.formError.set(applyServerErrors(this.form, error, 'errors.registerFailed'));
    } finally {
      this.submitting.set(false);
    }
  }
}
