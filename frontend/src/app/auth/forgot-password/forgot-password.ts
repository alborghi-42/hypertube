import { HttpErrorResponse } from '@angular/common/http';
import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { NonNullableFormBuilder, ReactiveFormsModule } from '@angular/forms';
import { RouterLink } from '@angular/router';
import { TranslocoPipe } from '@jsverse/transloco';

import { MessagePipe, msg, type Message } from '../../i18n/message';
import { applyServerErrors, controlError } from '../form-errors';
import { PasswordResetService } from '../password-reset.service';
import { EMAIL_VALIDATORS } from '../validation';

@Component({
  selector: 'app-forgot-password',
  imports: [ReactiveFormsModule, RouterLink, TranslocoPipe, MessagePipe],
  templateUrl: './forgot-password.html',
  styleUrl: '../auth-form.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ForgotPassword {
  private readonly resets = inject(PasswordResetService);

  readonly form = inject(NonNullableFormBuilder).group({
    email: ['', EMAIL_VALIDATORS],
  });
  readonly submitting = signal(false);
  readonly sent = signal(false);
  readonly formError = signal<Message | null>(null);

  readonly controlError = controlError;

  async submit(): Promise<void> {
    if (this.form.invalid || this.submitting()) {
      this.form.markAllAsTouched();
      return;
    }
    this.submitting.set(true);
    this.formError.set(null);
    try {
      await this.resets.forgot(this.form.getRawValue().email.trim());
      this.sent.set(true);
    } catch (error) {
      this.formError.set(
        error instanceof HttpErrorResponse && error.status === 429
          ? msg('errors.tooManyResets')
          : applyServerErrors(this.form, error, 'errors.requestFailed'),
      );
    } finally {
      this.submitting.set(false);
    }
  }
}
