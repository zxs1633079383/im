import { Injectable, inject } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';
import { API_BASE } from '../config/api.config';

export interface FileRecord {
  id: number;
  uploader_id: number;
  file_name: string;
  file_size: number;
  mime_type: string;
  thumbnail_path?: string;
  created_at: string;
}

export interface AttachmentsResponse {
  files: FileRecord[];
}

@Injectable({ providedIn: 'root' })
export class FileService {
  private http = inject(HttpClient);

  /** Upload a file via multipart/form-data. Returns the created FileRecord. */
  async upload(file: File): Promise<FileRecord> {
    const form = new FormData();
    form.append('file', file, file.name);
    return firstValueFrom(
      this.http.post<FileRecord>(`${API_BASE}/files`, form),
    );
  }

  /** Returns the public download URL for a file ID. */
  downloadUrl(fileId: number): string {
    return `${API_BASE}/files/${fileId}`;
  }

  /** Returns true if the mime type is an image type. */
  isImage(mimeType: string): boolean {
    return mimeType.startsWith('image/');
  }

  /** Returns true if the mime type is a video type. */
  isVideo(mimeType: string): boolean {
    return mimeType.startsWith('video/');
  }

  /** Fetch attachments for a given message ID. */
  async listAttachments(messageId: number): Promise<FileRecord[]> {
    const resp = await firstValueFrom(
      this.http.get<AttachmentsResponse>(`${API_BASE}/messages/${messageId}/attachments`),
    );
    return resp.files ?? [];
  }

  /** Format file size for display (e.g. "1.2 MB"). */
  formatSize(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  }
}
