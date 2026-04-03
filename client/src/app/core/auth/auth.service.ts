import { Injectable, signal, computed } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';

export interface User {
  id: number;
  username: string;
  email: string;
  display_name: string;
  avatar_url: string;
  status: number;
  created_at: string;
  updated_at: string;
}

export interface AuthResponse {
  token: string;
  user: User;
}

export interface RegisterPayload {
  username: string;
  email: string;
  password: string;
  display_name: string;
}

export interface LoginPayload {
  login: string;   // username or email
  password: string;
}

const TOKEN_KEY = 'im_auth_token';
const USER_KEY  = 'im_auth_user';
const API_BASE  = 'http://localhost:8080/api';

@Injectable({ providedIn: 'root' })
export class AuthService {
  private _token = signal<string | null>(localStorage.getItem(TOKEN_KEY));
  private _user  = signal<User | null>(this.loadStoredUser());

  /** Whether the user is currently authenticated. */
  readonly isAuthenticated = computed(() => this._token() !== null);

  /** The currently logged-in user, or null. */
  readonly currentUser = computed(() => this._user());

  /** The raw JWT token string, or null. */
  readonly token = computed(() => this._token());

  constructor(private http: HttpClient) {}

  async register(payload: RegisterPayload): Promise<AuthResponse> {
    const resp = await firstValueFrom(
      this.http.post<AuthResponse>(`${API_BASE}/auth/register`, payload)
    );
    this.storeAuth(resp);
    return resp;
  }

  async login(payload: LoginPayload): Promise<AuthResponse> {
    const resp = await firstValueFrom(
      this.http.post<AuthResponse>(`${API_BASE}/auth/login`, payload)
    );
    this.storeAuth(resp);
    return resp;
  }

  /** Update the current user's display_name and/or avatar_url. */
  async updateProfile(displayName: string, avatarURL: string): Promise<void> {
    const updated = await firstValueFrom(
      this.http.put<User>(`${API_BASE}/users/me`, { display_name: displayName, avatar_url: avatarURL }),
    );
    this._user.set(updated);
    localStorage.setItem(USER_KEY, JSON.stringify(updated));
  }

  async fetchCurrentUser(): Promise<User> {
    const user = await firstValueFrom(
      this.http.get<User>(`${API_BASE}/auth/me`)
    );
    this._user.set(user);
    localStorage.setItem(USER_KEY, JSON.stringify(user));
    return user;
  }

  logout(): void {
    this._token.set(null);
    this._user.set(null);
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(USER_KEY);
  }

  private storeAuth(resp: AuthResponse): void {
    this._token.set(resp.token);
    this._user.set(resp.user);
    localStorage.setItem(TOKEN_KEY, resp.token);
    localStorage.setItem(USER_KEY, JSON.stringify(resp.user));
  }

  private loadStoredUser(): User | null {
    const raw = localStorage.getItem(USER_KEY);
    if (!raw) return null;
    try {
      return JSON.parse(raw) as User;
    } catch {
      return null;
    }
  }
}
