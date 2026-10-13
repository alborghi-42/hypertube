import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { NonNullableFormBuilder, ReactiveFormsModule } from '@angular/forms';
import { Router, RouterLink } from '@angular/router';

import { AuthService } from '../auth.service';
import { OAuthButtons } from '../oauth/oauth-buttons';
import { applyServerErrors, controlError } from '../form-errors';
import {
  EMAIL_VALIDATORS,
  NAME_VALIDATORS,
  PASSWORD_VALIDATORS,
  USERNAME_VALIDATORS,
} from '../validation';

@Component({
  selector: 'app-register',
  imports: [ReactiveFormsModule, RouterLink, OAuthButtons],
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
  readonly formError = signal<string | null>(null);

  readonly controlError = controlError;
  readonly passwordHint = 'At least 8 characters, with a letter and a digit.';

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
      this.formError.set(applyServerErrors(this.form, error, 'Registration failed, please try again'));
    } finally {
      this.submitting.set(false);
    }
  }
}
