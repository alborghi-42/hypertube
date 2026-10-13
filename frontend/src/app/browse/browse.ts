import { isPlatformBrowser } from '@angular/common';
import {
  ChangeDetectionStrategy,
  Component,
  PLATFORM_ID,
  afterRenderEffect,
  computed,
  effect,
  inject,
  resource,
  signal,
  viewChild,
  type ElementRef,
} from '@angular/core';
import { RouterLink } from '@angular/router';
import { TranslocoPipe } from '@jsverse/transloco';

import { LanguageService } from '../shell/language.service';
import {
  DEFAULT_PROVIDER,
  MediaApi,
  type MediaDoc,
  type MediaListParams,
} from '../media/media-api';

interface Video {
  id: string;
  title: string;
  channel: string;
  provider: string;
  published: string;
  year: string;
  /** IMDb rating, null when unknown. */
  rating: number | null;
  genres: string[];
  watched: boolean;
  accent: string;
  thumbnailUrl: string;
}

export type SortKey = 'default' | 'name' | 'rating' | 'yearDesc' | 'yearAsc';

export const SORT_KEYS: readonly SortKey[] = ['default', 'name', 'rating', 'yearDesc', 'yearAsc'];

/** Library filters; null bounds and '' genre mean "any". */
export interface LibraryFilters {
  genre: string;
  minRating: number | null;
  maxRating: number | null;
  minYear: number | null;
  maxYear: number | null;
}

const NO_FILTERS: LibraryFilters = {
  genre: '',
  minRating: null,
  maxRating: null,
  minYear: null,
  maxYear: null,
};

const ROWS_PER_PAGE = 24;

/**
 * Browse page: infinite-scroll feed of legal media, search, sort and filters.
 * Ported from the deprecated Clashboard /hypertube page. Cards link to the
 * movie page (`/movie/:provider/:externalId`).
 */
@Component({
  selector: 'app-browse',
  imports: [RouterLink, TranslocoPipe],
  templateUrl: './browse.html',
  styleUrl: './browse.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Browse {
  private readonly api = inject(MediaApi);
  private readonly language = inject(LanguageService);
  private readonly isBrowser = isPlatformBrowser(inject(PLATFORM_ID));

  private searchDebounceTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly mediaPageCache = new Map<string, MediaDoc[]>();
  private readonly preloadingPageKeys = new Set<string>();
  private readonly preloadedThumbnailUrls = new Set<string>();

  private readonly sentinelRef = viewChild<ElementRef<HTMLElement>>('sentinel');

  readonly searchQuery = signal('');
  readonly debouncedSearchQuery = signal('');
  readonly sortKey = signal<SortKey>('default');
  readonly filters = signal<LibraryFilters>(NO_FILTERS);
  readonly sortKeys = SORT_KEYS;
  readonly currentPage = signal(1);
  readonly allDocs = signal<MediaDoc[]>([]);
  readonly hasMore = signal(true);
  /** Data is fetched in the browser only: the relative /api URLs have no origin during SSR/prerender. */
  readonly mediaResource = resource({
    params: (): MediaListParams | undefined =>
      this.isBrowser
        ? {
            page: this.currentPage(),
            query: this.debouncedSearchQuery().trim(),
            rows: ROWS_PER_PAGE,
          }
        : undefined,
    loader: async ({ params, abortSignal }) => {
      const cacheKey = cacheKeyOf(params);
      const cached = this.mediaPageCache.get(cacheKey);
      if (cached !== undefined) {
        return cached;
      }
      const docs = await this.api.list(params, abortSignal);
      this.mediaPageCache.set(cacheKey, docs);
      return docs;
    },
  });

  readonly isLoadingMore = computed(() => this.mediaResource.isLoading() && this.currentPage() > 1);

  readonly videos = computed(() =>
    this.allDocs()
      .filter((doc) => (doc.identifier ?? doc.external_id ?? '').trim().length > 0)
      .map((doc, index): Video => {
        const id = doc.identifier ?? doc.external_id ?? `archive-${index}`;
        const rawChannel = doc.provider?.trim() ?? '';
        const provider = rawChannel.length > 0 ? rawChannel : DEFAULT_PROVIDER;
        const rawTitle = doc.title?.trim() ?? '';
        return {
          id,
          title: rawTitle.length > 0 ? rawTitle : id,
          channel: rawChannel.length > 0 ? rawChannel : provider,
          provider,
          published: this.formatPublishedDate(doc.date),
          year: doc.year?.trim() || yearOf(doc.date),
          rating:
            typeof doc.imdb_rating === 'number' && doc.imdb_rating > 0 ? doc.imdb_rating : null,
          genres: doc.genres ?? [],
          watched: doc.watched === true,
          accent: '0.71 0.17 150',
          thumbnailUrl: doc.thumbnailUrl ?? doc.thumbnail_url ?? '',
        };
      }),
  );

  /** Genres present in the loaded results, for the genre filter. */
  readonly genres = computed(() =>
    [...new Set(this.videos().flatMap((video) => video.genres))].sort((a, b) =>
      a.localeCompare(b, this.language.current()),
    ),
  );

  readonly hasActiveFilters = computed(
    () =>
      this.sortKey() !== 'default' ||
      Object.values(this.filters()).some((value) => value !== null && value !== ''),
  );

  /**
   * Sorting and filtering run client-side on the results loaded so far: the providers page
   * independently and only OMDb knows ratings/genres, so the backend cannot sort or filter
   * the merged catalogue. Infinite scroll keeps loading pages (see the sentinel effect), so
   * matches from later pages are appended and placed in order as they arrive.
   */
  readonly filteredVideos = computed(() => {
    const { genre, minRating, maxRating, minYear, maxYear } = this.filters();
    const list = this.videos().filter((video) => {
      if (genre !== '' && !video.genres.includes(genre)) return false;
      if (minRating !== null || maxRating !== null) {
        if (video.rating === null) return false;
        if (minRating !== null && video.rating < minRating) return false;
        if (maxRating !== null && video.rating > maxRating) return false;
      }
      if (minYear !== null || maxYear !== null) {
        const year = Number.parseInt(video.year, 10);
        if (Number.isNaN(year)) return false;
        if (minYear !== null && year < minYear) return false;
        if (maxYear !== null && year > maxYear) return false;
      }
      return true;
    });
    return sortVideos(list, this.sortKey(), this.language.current());
  });

  constructor() {
    // Accumulate docs as each page loads.
    effect(() => {
      const newDocs = this.mediaResource.value();
      if (newDocs === undefined) return;

      this.preloadThumbnails(newDocs);
      if (newDocs.length < ROWS_PER_PAGE) {
        this.hasMore.set(false);
      }
      if (this.currentPage() === 1) {
        this.allDocs.set(newDocs);
      } else {
        this.allDocs.update((prev) => [...prev, ...newDocs]);
      }
      if (newDocs.length === ROWS_PER_PAGE) {
        void this.preloadNextPage({
          page: this.currentPage(),
          query: this.debouncedSearchQuery().trim(),
          rows: ROWS_PER_PAGE,
        });
      }
    });

    // Load the next page when the sentinel enters the viewport (afterRenderEffect is browser-only).
    afterRenderEffect((onCleanup) => {
      const sentinel = this.sentinelRef()?.nativeElement;
      if (!sentinel) return;
      // Re-observe whenever the visible list changes: a fresh observer reports the current
      // intersection, so when filters hide most cards the next page still loads.
      this.filteredVideos();

      const observer = new IntersectionObserver(
        (entries) => {
          if (entries[0]?.isIntersecting && !this.mediaResource.isLoading() && this.hasMore()) {
            this.currentPage.update((p) => p + 1);
          }
        },
        { rootMargin: '0px 0px 400px 0px', threshold: 0 },
      );
      observer.observe(sentinel);
      onCleanup(() => observer.disconnect());
    });
  }

  onSearchInput(event: Event): void {
    const nextValue = (event.target as HTMLInputElement).value;
    this.searchQuery.set(nextValue);

    if (this.searchDebounceTimer) {
      clearTimeout(this.searchDebounceTimer);
    }
    this.searchDebounceTimer = setTimeout(() => {
      this.debouncedSearchQuery.set(nextValue);
      this.resetFeed();
      this.searchDebounceTimer = null;
    }, 250);
  }

  onSortChange(event: Event): void {
    const value = (event.target as HTMLSelectElement).value as SortKey;
    this.sortKey.set(SORT_KEYS.includes(value) ? value : 'default');
  }

  onGenreChange(event: Event): void {
    const genre = (event.target as HTMLSelectElement).value;
    this.filters.update((f) => ({ ...f, genre }));
  }

  onRangeInput(key: Exclude<keyof LibraryFilters, 'genre'>, event: Event): void {
    const raw = (event.target as HTMLInputElement).value.trim();
    const value = raw === '' ? null : Number(raw);
    this.filters.update((f) => ({
      ...f,
      [key]: value !== null && Number.isFinite(value) ? value : null,
    }));
  }

  resetFilters(): void {
    this.sortKey.set('default');
    this.filters.set(NO_FILTERS);
  }

  reloadMedia(): void {
    this.resetFeed();
  }

  formatPublishedDate(date: string | undefined): string {
    const unknown = this.language.t('browse.unknownDate');
    if (date === undefined || date.trim().length === 0) {
      return unknown;
    }
    const parsed = new Date(date);
    return Number.isNaN(parsed.getTime())
      ? unknown
      : parsed.toLocaleDateString(this.language.current());
  }

  private preloadThumbnails(docs: MediaDoc[]): void {
    for (const doc of docs) {
      const url = (doc.thumbnailUrl ?? doc.thumbnail_url ?? '').trim();
      if (!url || this.preloadedThumbnailUrls.has(url)) {
        continue;
      }
      this.preloadedThumbnailUrls.add(url);
      const image = new Image();
      image.decoding = 'async';
      image.src = url;
    }
  }

  private async preloadNextPage(params: MediaListParams): Promise<void> {
    const next: MediaListParams = { ...params, page: params.page + 1 };
    const key = cacheKeyOf(next);
    if (this.mediaPageCache.has(key) || this.preloadingPageKeys.has(key)) {
      return;
    }
    this.preloadingPageKeys.add(key);
    try {
      const docs = await this.api.list(next);
      this.mediaPageCache.set(key, docs);
      this.preloadThumbnails(docs);
    } catch {
      // Prefetch is best-effort; the real page load will surface errors.
    } finally {
      this.preloadingPageKeys.delete(key);
    }
  }

  private resetFeed(): void {
    this.hasMore.set(true);
    this.allDocs.set([]);
    this.mediaPageCache.clear();
    this.preloadingPageKeys.clear();
    this.preloadedThumbnailUrls.clear();
    const wasAlreadyPage1 = this.currentPage() === 1;
    this.currentPage.set(1);
    if (wasAlreadyPage1) {
      this.mediaResource.reload();
    }
  }
}

/** Sorted copy of list; 'default' keeps the backend order. Unknown values sort last. */
function sortVideos(list: Video[], key: SortKey, locale: string): Video[] {
  if (key === 'default') {
    return list;
  }
  const byName = (a: Video, b: Video) =>
    a.title.localeCompare(b.title, locale, { sensitivity: 'base' });
  const year = (video: Video) => Number.parseInt(video.year, 10);
  const compare = (a: number, b: number, desc: boolean) => {
    const na = Number.isNaN(a);
    const nb = Number.isNaN(b);
    if (na || nb) return na === nb ? 0 : na ? 1 : -1;
    return desc ? b - a : a - b;
  };
  const cmp: Record<Exclude<SortKey, 'default'>, (a: Video, b: Video) => number> = {
    name: byName,
    rating: (a, b) => compare(a.rating ?? Number.NaN, b.rating ?? Number.NaN, true) || byName(a, b),
    yearDesc: (a, b) => compare(year(a), year(b), true) || byName(a, b),
    yearAsc: (a, b) => compare(year(a), year(b), false) || byName(a, b),
  };
  return [...list].sort(cmp[key]);
}

/** The 4-digit year at the start of a provider date, or "". */
function yearOf(date: string | undefined): string {
  const match = /^(\d{4})/.exec(date?.trim() ?? '');
  return match?.[1] ?? '';
}

function cacheKeyOf(params: MediaListParams): string {
  return `${params.query}::${params.page}`;
}
