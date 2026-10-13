import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { ActivatedRoute } from '@angular/router';
import { TranslocoPipe } from '@jsverse/transloco';

import { safeReturnUrl } from '../auth.service';

/** ?oauth_error codes with a dedicated message (translation keys under oauth.errors). */
const OAUTH_ERRORS = new Set(['denied', 'state', 'email', 'unavailable']);

/**
 * "Log in with 42 / GitHub / Google". Plain links (full-page navigation) to the backend start
 * endpoint, relative to <base href> so they follow the deploy prefix. Also shows the
 * error the callback redirects back with (?oauth_error=...).
 */
@Component({
  selector: 'app-oauth-buttons',
  imports: [TranslocoPipe],
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
    return code ? `oauth.errors.${OAUTH_ERRORS.has(code) ? code : 'generic'}` : null;
  });
}
