import { isPlatformBrowser } from '@angular/common';
import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  ElementRef,
  PLATFORM_ID,
  afterRenderEffect,
  computed,
  inject,
  input,
  linkedSignal,
  output,
  resource,
  signal,
  viewChild,
} from '@angular/core';
import { TranslocoPipe } from '@jsverse/transloco';

import { LanguageService } from '../../shell/language.service';
import { MediaApi, type SubtitleTrack } from '../media-api';

/** Delays (ms) of the subtitle polls after play: the server fetches them in the background. */
const SUBTITLE_POLLS = [4000, 12000, 30000];

/**
 * Video player for one media item: asks the backend to make it playable (/api/media/play,
 * which starts the torrent download if needed) and streams the returned URL. Browser only:
 * nothing is requested during SSR. Subtitle tracks (WebVTT) come with the play response and,
 * since the server fetches them in the background, from a few follow-up polls.
 */
@Component({
  selector: 'app-media-player',
  imports: [TranslocoPipe],
  templateUrl: './media-player.html',
  styleUrl: './media-player.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MediaPlayer {
  private readonly api = inject(MediaApi);
  private readonly isBrowser = isPlatformBrowser(inject(PLATFORM_ID));
  private readonly language = inject(LanguageService);
  private readonly video = viewChild<ElementRef<HTMLVideoElement>>('video');
  /** Bumped on every new stream so a stale subtitle poll drops its result. */
  private pollGeneration = 0;
  private pollTimer: ReturnType<typeof setTimeout> | undefined;

  readonly externalId = input.required<string>();
  readonly provider = input.required<string>();
  /** Backdrop shown behind the play button before playback starts. */
  readonly poster = input('');

  /** Emitted once the backend accepted the play (it then counts the film as watched). */
  readonly started = output<void>();

  /**
   * Playback (and so the server-side download and the "watched" mark) starts on the user's
   * click, not on page load. Reset when the page switches to another film.
   */
  readonly requested = linkedSignal({
    source: () => `${this.provider()}\u0000${this.externalId()}`,
    computation: () => false,
  });

  readonly stream = resource({
    params: () =>
      this.isBrowser && this.requested()
        ? { externalId: this.externalId(), provider: this.provider() }
        : undefined,
    loader: async ({ params }) => {
      this.stopPolling();
      this.subtitles.set([]);
      const result = await this.api.playStream(params.externalId, params.provider);
      if (result === null) {
        throw new Error('stream unavailable');
      }
      this.started.emit();
      this.subtitles.set(result.subtitles ?? []);
      this.pollSubtitles(result.internal_id);
      return result;
    },
  });

  /** WebVTT tracks of the current stream; filled in while the server fetches them. */
  readonly subtitles = signal<SubtitleTrack[]>([]);
  /** True while the follow-up subtitle polls are still running. */
  readonly subtitlesPending = signal(false);

  /** The track shown by default: the UI language if available, else English. */
  readonly defaultSubtitle = computed(() => {
    const tracks = this.subtitles();
    const lang = this.language.current();
    return (tracks.find((t) => t.lang === lang) ?? tracks.find((t) => t.lang === 'en'))?.lang;
  });

  /** The backend answered 202: playback starts but may stall while pieces arrive. */
  readonly downloading = computed(() => this.stream.value()?.status === 'downloading');
  readonly transcoded = computed(() => this.stream.value()?.transcoded === true);

  constructor() {
    inject(DestroyRef).onDestroy(() => this.stopPolling());
    // A <track default> added after the video started is not always activated by the
    // browser: show the default one, unless the user already picked a track.
    afterRenderEffect(() => {
      const lang = this.defaultSubtitle();
      const tracks = this.video()?.nativeElement.textTracks;
      if (lang === undefined || tracks === undefined) {
        return;
      }
      const list = Array.from(tracks);
      if (list.some((t) => t.mode === 'showing')) {
        return;
      }
      const track = list.find((t) => t.language === lang);
      if (track !== undefined) {
        track.mode = 'showing';
      }
    });
  }

  start(): void {
    this.requested.set(true);
  }

  retry(): void {
    this.stream.reload();
  }

  private stopPolling(): void {
    this.pollGeneration++;
    clearTimeout(this.pollTimer);
    this.subtitlesPending.set(false);
  }

  /** Asks for the subtitle list a few times while the server reports it is still fetching. */
  private pollSubtitles(mediaId: string | undefined): void {
    this.stopPolling();
    if (mediaId === undefined || mediaId === '') {
      return;
    }
    const generation = this.pollGeneration;
    const poll = (attempt: number): void => {
      if (attempt >= SUBTITLE_POLLS.length) {
        this.subtitlesPending.set(false);
        return;
      }
      this.pollTimer = setTimeout(() => {
        void this.api.subtitles(mediaId).then((list) => {
          if (generation !== this.pollGeneration) {
            return;
          }
          if (list !== null) {
            this.subtitles.set(list.subtitles);
            if (!list.pending) {
              this.subtitlesPending.set(false);
              return;
            }
          }
          poll(attempt + 1);
        });
      }, SUBTITLE_POLLS[attempt]);
    };
    this.subtitlesPending.set(true);
    poll(0);
  }
}
