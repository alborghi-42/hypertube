import {
  ChangeDetectionStrategy,
  Component,
  computed,
  inject,
  input,
  resource,
  signal,
} from '@angular/core';
import { RouterLink } from '@angular/router';
import { TranslocoPipe } from '@jsverse/transloco';

import { AuthService } from '../../auth/auth.service';
import { LanguageService } from '../../shell/language.service';
import { CommentsApi, MAX_COMMENT_LENGTH, type MediaComment } from '../comments-api';

/** Length in characters (code points), as the backend counts runes. */
function commentLength(text: string): number {
  return [...text.trim()].length;
}

/** Non-empty after trimming and at most MAX_COMMENT_LENGTH characters, like the backend. */
function isValidComment(text: string): boolean {
  const length = commentLength(text);
  return length > 0 && length <= MAX_COMMENT_LENGTH;
}

/** Comment thread of one media item. Only the author sees edit/delete on a comment. */
@Component({
  selector: 'app-media-comments',
  imports: [RouterLink, TranslocoPipe],
  templateUrl: './media-comments.html',
  styleUrl: './media-comments.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MediaComments {
  private readonly api = inject(CommentsApi);
  private readonly auth = inject(AuthService);
  private readonly language = inject(LanguageService);

  /** Internal media id (UUID) as returned by /api/media/details. */
  readonly mediaId = input.required<string>();

  readonly maxLength = MAX_COMMENT_LENGTH;
  readonly draft = signal('');
  readonly editingId = signal<string | null>(null);
  readonly editDraft = signal('');
  readonly busy = signal(false);
  /** Translation key of the last failed action. */
  readonly actionError = signal<string | null>(null);

  readonly commentsResource = resource({
    params: () => ({ mediaId: this.mediaId() }),
    loader: ({ params }) => this.api.list(params.mediaId),
  });

  readonly comments = computed(() => this.commentsResource.value() ?? []);
  readonly canPost = computed(() => !this.busy() && isValidComment(this.draft()));
  readonly canSaveEdit = computed(() => !this.busy() && isValidComment(this.editDraft()));

  isOwn(comment: MediaComment): boolean {
    return comment.user_id === this.auth.user()?.id;
  }

  remaining(text: string): number {
    return this.maxLength - commentLength(text);
  }

  onDraftInput(event: Event): void {
    this.draft.set((event.target as HTMLTextAreaElement).value);
  }

  onEditInput(event: Event): void {
    this.editDraft.set((event.target as HTMLTextAreaElement).value);
  }

  async post(): Promise<void> {
    if (!this.canPost()) return;
    await this.run(async () => {
      const created = await this.api.create(this.mediaId(), this.draft().trim());
      this.commentsResource.update((list) => [created, ...(list ?? [])]);
      this.draft.set('');
    }, 'comments.postFailed');
  }

  startEdit(comment: MediaComment): void {
    this.editingId.set(comment.id);
    this.editDraft.set(comment.content);
    this.actionError.set(null);
  }

  cancelEdit(): void {
    this.editingId.set(null);
  }

  async saveEdit(comment: MediaComment): Promise<void> {
    if (!this.canSaveEdit()) return;
    await this.run(async () => {
      const updated = await this.api.update(this.mediaId(), comment.id, this.editDraft().trim());
      this.commentsResource.update((list) =>
        (list ?? []).map((c) => (c.id === updated.id ? updated : c)),
      );
      this.editingId.set(null);
    }, 'comments.saveFailed');
  }

  async remove(comment: MediaComment): Promise<void> {
    if (this.busy()) return;
    await this.run(async () => {
      await this.api.remove(this.mediaId(), comment.id);
      this.commentsResource.update((list) => (list ?? []).filter((c) => c.id !== comment.id));
    }, 'comments.deleteFailed');
  }

  formatDate(value: string): string {
    const parsed = new Date(value);
    return Number.isNaN(parsed.getTime()) ? '' : parsed.toLocaleString(this.language.current());
  }

  private async run(action: () => Promise<void>, failure: string): Promise<void> {
    this.busy.set(true);
    this.actionError.set(null);
    try {
      await action();
    } catch {
      this.actionError.set(failure);
    } finally {
      this.busy.set(false);
    }
  }
}
