import { Component, OnInit, inject, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { AuthService } from '../../core/auth/auth.service';

@Component({
  selector: 'app-profile',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './profile.component.html',
  styleUrls: ['./profile.component.scss'],
})
export class ProfileComponent implements OnInit {
  private authService = inject(AuthService);

  user = this.authService.currentUser;

  displayName = signal('');
  avatarURL = signal('');
  saving = signal(false);
  success = signal(false);
  error = signal<string | null>(null);

  ngOnInit(): void {
    const u = this.user();
    if (u) {
      this.displayName.set(u.display_name);
      this.avatarURL.set(u.avatar_url ?? '');
    }
  }

  async save(): Promise<void> {
    this.saving.set(true);
    this.success.set(false);
    this.error.set(null);
    try {
      await this.authService.updateProfile(this.displayName(), this.avatarURL());
      this.success.set(true);
    } catch {
      this.error.set('Failed to update profile. Please try again.');
    } finally {
      this.saving.set(false);
    }
  }

  get hasChanges(): boolean {
    const u = this.user();
    if (!u) return false;
    return this.displayName() !== u.display_name || this.avatarURL() !== (u.avatar_url ?? '');
  }
}
