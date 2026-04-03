import { Component, OnInit, inject, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Router } from '@angular/router';
import { FavoriteService, FavoriteWithMessage } from '../../core/favorites/favorite.service';

@Component({
  selector: 'app-favorites',
  standalone: true,
  imports: [CommonModule],
  templateUrl: './favorites.component.html',
  styleUrls: ['./favorites.component.scss'],
})
export class FavoritesComponent implements OnInit {
  private favoriteService = inject(FavoriteService);
  private router = inject(Router);

  favorites = this.favoriteService.favorites;
  loading = signal(false);
  error = signal<string | null>(null);

  async ngOnInit(): Promise<void> {
    this.loading.set(true);
    try {
      await this.favoriteService.load();
    } catch {
      this.error.set('Failed to load favorites.');
    } finally {
      this.loading.set(false);
    }
  }

  jumpToMessage(fav: FavoriteWithMessage): void {
    this.router.navigate(['/channels', fav.message.channel_id], {
      queryParams: { around_seq: fav.message.seq },
    });
  }

  async removeFavorite(fav: FavoriteWithMessage): Promise<void> {
    try {
      await this.favoriteService.remove(fav.message_id);
    } catch { /* TODO: toast */ }
  }
}
