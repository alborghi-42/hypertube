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

/** The untouched rendition: range streaming, or the plain on-the-fly conversion. */
const QUALITY_ORIGINAL = 'original';

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
  /** The stream response; undefined while loading or on error (value() would throw). */
  readonly current = computed(() => (this.stream.hasValue() ? this.stream.value() : undefined));
  readonly downloading = computed(() => this.current()?.status === 'downloading');
  readonly transcoded = computed(() => this.current()?.transcoded === true);

  /** `?quality=` values offered by the server; "original" alone hides the selector. */
  readonly qualities = computed(() => {
    const list = this.current()?.qualities ?? [];
    return list.length > 0 ? list : [QUALITY_ORIGINAL];
  });
  /** Selected rendition; back to original for every new stream. */
  readonly quality = linkedSignal({
    source: () => this.current()?.url,
    computation: () => QUALITY_ORIGINAL,
  });
  /** Absolute start (s) of the current server-encoded stream, sent as `?t=`. */
  readonly startAt = linkedSignal({ source: () => this.current()?.url, computation: () => 0 });
  /** Server-encoded stream (mkv conversion or a downscaled rendition): seek by reloading. */
  readonly serverSeek = computed(() => this.transcoded() || this.quality() !== QUALITY_ORIGINAL);
  /** Set when a server-encoded stream fails (typically 503: all conversion slots busy). */
  readonly streamFailed = signal(false);

  /** The <video> src: the play url plus `?quality=` / `?t=` for server-encoded streams. */
  readonly src = computed(() => {
    const base = this.current()?.url;
    if (base === undefined) {
      return '';
    }
    const params: string[] = [];
    if (this.quality() !== QUALITY_ORIGINAL) {
      params.push(`quality=${encodeURIComponent(this.quality())}`);
    }
    if (this.serverSeek() && this.startAt() > 0) {
      params.push(`t=${this.startAt()}`);
    }
    return params.length === 0
      ? base
      : `${base}${base.includes('?') ? '&' : '?'}${params.join('&')}`;
  });

  /**
   * Offset between the film's timeline and the element's currentTime. The server shifts
   * `?t=` streams with -output_ts_offset so this stays 0; if a browser still rebases the
   * piped stream to 0, it becomes startAt and the subtitle cues are shifted to match.
   */
  private timeBase = 0;
  /** First timeupdate of a `?t=` stream decides timeBase. */
  private detectBase = false;
  /** Position to restore with currentTime once a range-seekable stream has metadata. */
  private pendingSeek = 0;
  /** Seconds each TextTrack's cues are currently shifted by. */
  private cueShift = new WeakMap<TextTrack, number>();

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

  /** Switches rendition, keeping the playback position. */
  selectQuality(value: string): void {
    if (value === this.quality() || !this.qualities().includes(value)) {
      return;
    }
    const position = this.position();
    this.quality.set(value);
    this.reloadAt(position);
  }

  /** Restarts the current server-encoded stream, or the original, at the given second. */
  private reloadAt(position: number): void {
    this.streamFailed.set(false);
    const seconds = Math.max(0, Math.floor(position));
    if (this.serverSeek()) {
      this.pendingSeek = 0;
      this.startAt.set(seconds);
      this.detectBase = seconds > 0;
      this.timeBase = 0;
    } else {
      this.startAt.set(0);
      this.detectBase = false;
      this.timeBase = 0;
      this.pendingSeek = position;
    }
    this.applyCueShift();
  }

  /** Current position on the film's timeline. */
  private position(): number {
    const el = this.video()?.nativeElement;
    return el === undefined ? 0 : this.timeBase + el.currentTime;
  }

  onLoadedMetadata(): void {
    const el = this.video()?.nativeElement;
    if (el !== undefined && this.pendingSeek > 0) {
      el.currentTime = this.pendingSeek;
      this.pendingSeek = 0;
    }
  }

  onTimeUpdate(): void {
    const el = this.video()?.nativeElement;
    if (!this.detectBase || el === undefined || el.currentTime <= 0) {
      return;
    }
    this.detectBase = false;
    const start = this.startAt();
    // Absolute timeline (the -output_ts_offset worked): currentTime is near start.
    this.timeBase = el.currentTime >= start - 2 ? 0 : start;
    this.applyCueShift();
  }

  /**
   * A server-encoded stream cannot be range-seeked: a seek outside what is already buffered
   * reloads it from the server at that position (`?t=`).
   */
  onSeeking(): void {
    const el = this.video()?.nativeElement;
    if (el === undefined || !this.serverSeek() || this.detectBase) {
      return;
    }
    const target = el.currentTime;
    const buffered = el.buffered;
    for (let i = 0; i < buffered.length; i++) {
      if (target >= buffered.start(i) && target <= buffered.end(i)) {
        return;
      }
    }
    this.reloadAt(this.timeBase + target);
  }

  onVideoError(): void {
    if (this.serverSeek()) {
      this.streamFailed.set(true);
    }
  }

  /** A <track> (re)loaded its cues: they start unshifted. */
  onTrackLoad(event: Event): void {
    const track = (event.target as HTMLTrackElement | null)?.track;
    if (track !== undefined) {
      this.cueShift.set(track, 0);
    }
    this.applyCueShift();
  }

  /** Moves every loaded cue so it matches the element's timeline (film time - timeBase). */
  private applyCueShift(): void {
    const tracks = this.video()?.nativeElement.textTracks;
    if (tracks === undefined) {
      return;
    }
    const wanted = -this.timeBase;
    for (const track of Array.from(tracks)) {
      const cues = track.cues;
      if (cues === null || cues.length === 0) {
        continue;
      }
      const delta = wanted - (this.cueShift.get(track) ?? 0);
      if (delta !== 0) {
        for (const cue of Array.from(cues)) {
          cue.startTime += delta;
          cue.endTime += delta;
        }
      }
      this.cueShift.set(track, wanted);
    }
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
