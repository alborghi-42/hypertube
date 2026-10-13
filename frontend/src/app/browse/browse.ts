import { Location, isPlatformBrowser } from '@angular/common';
import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
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
import { ActivatedRoute } from '@angular/router';
import { TranslocoPipe } from '@jsverse/transloco';

import { MediaComments } from '../media/comments/media-comments';
import { LanguageService } from '../shell/language.service';
import {
  DEFAULT_PROVIDER,
  MediaApi,
  type MediaDetails,
  type MediaDoc,
  type MediaListParams,
} from '../media/media-api';

interface Video {
  id: string;
  title: string;
  channel: string;
  provider: string;
  published: string;
  category: string;
  accent: string;
  thumbnailUrl: string;
  detailsUrl: string;
}

const ROWS_PER_PAGE = 24;
const WATCH_PATH = /^\/watch\/([^/]+)$/;

/**
 * Browse page: infinite-scroll feed of legal media (archive.org), search, and a
 * player modal. Ported from the deprecated Clashboard /hypertube page.
 * Routes: `/` (feed) and `/watch/:id` (feed with the player open).
 */
@Component({
  selector: 'app-browse',
  imports: [MediaComments, TranslocoPipe],
  templateUrl: './browse.html',
  styleUrl: './browse.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    '(document:keydown.escape)': 'closeVideoModal()',
  },
})
export class Browse {
  private readonly api = inject(MediaApi);
  private readonly location = inject(Location);
  private readonly language = inject(LanguageService);
  private readonly isBrowser = isPlatformBrowser(inject(PLATFORM_ID));

  private searchDebounceTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly mediaPageCache = new Map<string, MediaDoc[]>();
  private readonly mediaDetailsCache = new Map<string, MediaDetails>();
  private readonly preloadingPageKeys = new Set<string>();
  private readonly preloadedThumbnailUrls = new Set<string>();

  private readonly sentinelRef = viewChild<ElementRef<HTMLElement>>('sentinel');

  readonly searchQuery = signal('');
  readonly debouncedSearchQuery = signal('');
  readonly selectedCategory = signal('All');
  readonly currentPage = signal(1);
  readonly allDocs = signal<MediaDoc[]>([]);
  readonly hasMore = signal(true);
  readonly selectedVideoId = signal<string | null>(
    inject(ActivatedRoute).snapshot.paramMap.get('id'),
  );
  readonly streamUrlByVideoId = signal<Record<string, string>>({});
  readonly streamErrorByVideoId = signal<Record<string, boolean>>({});

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
        const rawCategory = doc.format?.trim() ?? '';
        return {
          id,
          title: rawTitle.length > 0 ? rawTitle : id,
          channel: rawChannel.length > 0 ? rawChannel : provider,
          provider,
          published: this.formatPublishedDate(doc.date),
          category:
            rawCategory.length > 0 ? rawCategory : this.language.t('browse.archiveCategory'),
          accent: '0.71 0.17 150',
          thumbnailUrl: doc.thumbnailUrl ?? doc.thumbnail_url ?? '',
          detailsUrl: sourceUrl(provider, id),
        };
      }),
  );

  readonly selectedVideo = computed(() => {
    const id = this.selectedVideoId();
    return id === null ? null : (this.videos().find((video) => video.id === id) ?? null);
  });

  readonly selectedVideoStreamUrl = computed(() => {
    const id = this.selectedVideoId();
    return id === null ? null : (this.streamUrlByVideoId()[id] ?? null);
  });

  readonly selectedVideoHasStreamError = computed(() => {
    const id = this.selectedVideoId();
    return id === null ? false : (this.streamErrorByVideoId()[id] ?? false);
  });

  readonly selectedVideoDetailsResource = resource({
    params: () => {
      const video = this.selectedVideo();
      return video ? { id: video.id, provider: video.provider } : undefined;
    },
    loader: async ({ params, abortSignal }) => {
      const cached = this.mediaDetailsCache.get(params.id);
      if (cached !== undefined) {
        return cached;
      }
      const details = await this.api.details(params.id, params.provider, abortSignal);
      this.mediaDetailsCache.set(params.id, details);
      return details;
    },
  });

  readonly selectedVideoDetails = computed(() => this.selectedVideoDetailsResource.value());

  readonly categories = computed(() => [
    'All',
    ...new Set(this.videos().map((video) => video.category)),
  ]);

  readonly effectiveCategory = computed(() => {
    const category = this.selectedCategory();
    return this.categories().includes(category) ? category : 'All';
  });

  readonly filteredVideos = computed(() => {
    const category = this.effectiveCategory();
    return this.videos().filter((video) => category === 'All' || video.category === category);
  });

  constructor() {
    const initialId = this.selectedVideoId();
    if (this.isBrowser && initialId !== null) {
      void this.loadVideoStream(initialId);
    }

    // Back/forward between `/` and `/watch/:id` (URL changes made with Location.go
    // are invisible to the router, so sync the modal from the path ourselves).
    const popstate = this.location.subscribe(({ url }) =>
      this.syncSelectedVideoFromPath(url ?? ''),
    );
    inject(DestroyRef).onDestroy(() => popstate.unsubscribe());

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

  selectCategory(category: string): void {
    this.selectedCategory.set(category);
  }

  openVideoModal(videoId: string): void {
    this.selectedVideoId.set(videoId);
    this.location.go(`/watch/${encodeURIComponent(videoId)}`);
    void this.loadVideoStream(videoId);
  }

  closeVideoModal(): void {
    if (this.selectedVideoId() === null) {
      return;
    }
    this.selectedVideoId.set(null);
    this.location.go('/');
  }

  onModalBackdropClick(event: Event): void {
    if (event.target === event.currentTarget) {
      this.closeVideoModal();
    }
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

  formatDuration(seconds: number | undefined): string {
    if (seconds === undefined || !Number.isFinite(seconds) || seconds <= 0) {
      return this.language.t('browse.unknownDuration');
    }
    const total = Math.floor(seconds);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const secs = String(total % 60).padStart(2, '0');
    return hours > 0
      ? `${hours}:${String(minutes).padStart(2, '0')}:${secs}`
      : `${minutes}:${secs}`;
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

  private async loadVideoStream(videoId: string): Promise<void> {
    // Keep fetched stream URLs to avoid redundant play calls.
    if (this.streamUrlByVideoId()[videoId]) {
      return;
    }
    const provider =
      this.videos().find((video) => video.id === videoId)?.provider ?? DEFAULT_PROVIDER;

    this.streamErrorByVideoId.update((errors) => ({ ...errors, [videoId]: false }));
    const url = await this.api.play(videoId, provider);
    if (url === null) {
      this.streamErrorByVideoId.update((errors) => ({ ...errors, [videoId]: true }));
      return;
    }
    this.streamUrlByVideoId.update((byId) => ({ ...byId, [videoId]: url }));
  }

  private syncSelectedVideoFromPath(url: string): void {
    const path = url.split(/[?#]/)[0].replace(/\/+$/, '');
    const match = WATCH_PATH.exec(path);
    let videoId: string | null = null;
    if (match?.[1] !== undefined) {
      try {
        videoId = decodeURIComponent(match[1]);
      } catch {
        videoId = null;
      }
    }
    this.selectedVideoId.set(videoId);
    if (videoId !== null) {
      void this.loadVideoStream(videoId);
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

function cacheKeyOf(params: MediaListParams): string {
  return `${params.query}::${params.page}`;
}

/** The item's page on its source site. */
function sourceUrl(provider: string, id: string): string {
  return provider === 'publicdomaintorrents'
    ? `https://www.publicdomaintorrents.info/nshowmovie.html?movieid=${encodeURIComponent(id)}`
    : `https://archive.org/details/${encodeURIComponent(id)}`;
}
