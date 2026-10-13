import { isPlatformBrowser } from '@angular/common';
import {
  ChangeDetectionStrategy,
  Component,
  PLATFORM_ID,
  computed,
  inject,
  input,
  linkedSignal,
  output,
  resource,
} from '@angular/core';
import { TranslocoPipe } from '@jsverse/transloco';

import { MediaApi } from '../media-api';

/**
 * Video player for one media item: asks the backend to make it playable (/api/media/play,
 * which starts the torrent download if needed) and streams the returned URL. Browser only:
 * nothing is requested during SSR.
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
      const result = await this.api.playStream(params.externalId, params.provider);
      if (result === null) {
        throw new Error('stream unavailable');
      }
      this.started.emit();
      return result;
    },
  });

  /** The backend answered 202: playback starts but may stall while pieces arrive. */
  readonly downloading = computed(() => this.stream.value()?.status === 'downloading');
  readonly transcoded = computed(() => this.stream.value()?.transcoded === true);

  start(): void {
    this.requested.set(true);
  }

  retry(): void {
    this.stream.reload();
  }
}
