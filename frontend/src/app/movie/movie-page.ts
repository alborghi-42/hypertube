import { isPlatformBrowser } from '@angular/common';
import {
  ChangeDetectionStrategy,
  Component,
  PLATFORM_ID,
  computed,
  inject,
  resource,
} from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { ActivatedRoute, RouterLink } from '@angular/router';
import { TranslocoPipe } from '@jsverse/transloco';

import { MediaComments } from '../media/comments/media-comments';
import { MediaApi } from '../media/media-api';
import { MediaPlayer } from '../media/player/media-player';
import { LanguageService } from '../shell/language.service';

/**
 * Movie page, `/movie/:provider/:externalId`: details, player and comments of one film.
 * Loads its own data from /api/media/details (which fetches and caches the item on first
 * access), so deep links work for films that are not in the loaded library feed.
 * Data is fetched in the browser only (relative /api URLs have no origin during SSR).
 */
@Component({
  selector: 'app-movie-page',
  imports: [MediaComments, MediaPlayer, RouterLink, TranslocoPipe],
  templateUrl: './movie-page.html',
  styleUrl: './movie-page.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MoviePage {
  private readonly api = inject(MediaApi);
  private readonly language = inject(LanguageService);
  private readonly isBrowser = isPlatformBrowser(inject(PLATFORM_ID));

  private readonly params = toSignal(inject(ActivatedRoute).paramMap, {
    initialValue: inject(ActivatedRoute).snapshot.paramMap,
  });
  readonly provider = computed(() => this.params().get('provider') ?? '');
  readonly externalId = computed(() => this.params().get('externalId') ?? '');

  readonly details = resource({
    params: () =>
      this.isBrowser && this.provider() && this.externalId()
        ? { provider: this.provider(), externalId: this.externalId() }
        : undefined,
    loader: ({ params, abortSignal }) =>
      this.api.details(params.externalId, params.provider, abortSignal),
  });

  readonly movie = computed(() => {
    const d = this.details.value();
    if (d === undefined) return null;
    const title = d.title?.trim() || this.externalId();
    return {
      id: d.id ?? '',
      title,
      summary: d.description?.trim() ?? '',
      cover: d.poster_url?.trim() || d.thumbnail_url?.trim() || '',
      year: d.year?.trim() || /^(\d{4})/.exec(d.date ?? '')?.[1] || '',
      length: this.formatLength(d.duration_seconds),
      rating: typeof d.imdb_rating === 'number' && d.imdb_rating > 0 ? d.imdb_rating : null,
      imdbId: d.imdb_id?.trim() ?? '',
      genres: d.genres ?? [],
      directors: d.directors ?? [],
      writers: d.writers ?? [],
      production: d.production?.trim() ?? '',
      cast: d.actors ?? [],
      creator: d.creator?.trim() ?? '',
      sourceUrl: sourceUrl(this.provider(), this.externalId()),
    };
  });

  /** "1 h 34 min" style, or '' when unknown. */
  private formatLength(seconds: number | undefined): string {
    if (seconds === undefined || !Number.isFinite(seconds) || seconds <= 0) {
      return '';
    }
    const minutes = Math.round(seconds / 60);
    const hours = Math.floor(minutes / 60);
    return hours > 0
      ? this.language.t('movie.lengthHours', { hours, minutes: minutes % 60 })
      : this.language.t('movie.lengthMinutes', { minutes });
  }
}

/** The item's page on its source site. */
function sourceUrl(provider: string, id: string): string {
  return provider === 'publicdomaintorrents'
    ? `https://www.publicdomaintorrents.info/nshowmovie.html?movieid=${encodeURIComponent(id)}`
    : `https://archive.org/details/${encodeURIComponent(id)}`;
}
