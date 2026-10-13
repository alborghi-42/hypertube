import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { ActivatedRoute } from '@angular/router';

import { safeReturnUrl } from '../auth.service';

const OAUTH_ERRORS: Record<string, string> = {
  denied: 'Login was cancelled.',
  state: 'The login link expired, please try again.',
  email: 'Your account has no verified email address, so it cannot be used to log in.',
  unavailable: 'This login method is not available right now.',
};

/**
 * "Log in with 42 / GitHub". Plain links (full-page navigation) to the backend start
 * endpoint, relative to <base href> so they follow the deploy prefix. Also shows the
 * error the callback redirects back with (?oauth_error=...).
 */
@Component({
  selector: 'app-oauth-buttons',
  templateUrl: './oauth-buttons.html',
  styleUrl: './oauth.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class OAuthButtons {
  private readonly query = toSignal(inject(ActivatedRoute).queryParamMap);

  readonly returnUrl = computed(() =>
    encodeURIComponent(safeReturnUrl(this.query()?.get('returnUrl'))),
  );
  readonly error = computed(() => {
    const code = this.query()?.get('oauth_error');
    return code ? (OAUTH_ERRORS[code] ?? 'Login failed, please try again.') : null;
  });
}
