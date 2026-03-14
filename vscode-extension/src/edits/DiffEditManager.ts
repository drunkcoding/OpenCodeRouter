import * as path from 'path';
import * as vscode from 'vscode';

export interface DiffCommandPayload {
  sessionId?: string;
  diff?: string;
  source?: string;
}

interface Hunk {
  oldStart: number;
  oldCount: number;
  newStart: number;
  newCount: number;
  lines: string[];
}

interface FilePatch {
  oldPath: string;
  newPath: string;
  isCreate: boolean;
  isDelete: boolean;
  hunks: Hunk[];
}

interface LineRange {
  startLine: number;
  endLine: number;
}

interface PendingDiffFile {
  path: string;
  uri: vscode.Uri;
  originalSnapshot: string;
  proposedContent: string;
  isCreate: boolean;
  isDelete: boolean;
  modifiedRanges: LineRange[];
}

interface PendingDiff {
  sessionId?: string;
  source: string;
  createdAt: number;
  rawDiff: string;
  files: PendingDiffFile[];
}

type WorkspaceResolver = (sessionId?: string) => string | undefined;

const decoder = new TextDecoder();
const encoder = new TextEncoder();

export class DiffEditManager implements vscode.Disposable {
  private pending?: PendingDiff;
  private readonly decorationType: vscode.TextEditorDecorationType;
  private readonly disposables: vscode.Disposable[] = [];
  private readonly decorationRanges = new Map<string, vscode.Range[]>();
  private clearTimer?: NodeJS.Timeout;

  constructor(private readonly resolveWorkspacePath: WorkspaceResolver) {
    this.decorationType = vscode.window.createTextEditorDecorationType({
      isWholeLine: true,
      backgroundColor: new vscode.ThemeColor('editor.wordHighlightStrongBackground'),
      overviewRulerColor: new vscode.ThemeColor('editorOverviewRuler.modifiedForeground'),
      overviewRulerLane: vscode.OverviewRulerLane.Right
    });

    this.disposables.push(
      vscode.window.onDidChangeVisibleTextEditors(() => {
        this.applyDecorationsToVisibleEditors();
      })
    );
  }

  dispose(): void {
    this.clearPending(false);
    if (this.clearTimer) {
      clearTimeout(this.clearTimer);
      this.clearTimer = undefined;
    }
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
    this.decorationType.dispose();
  }

  async stageFromPayload(payload?: DiffCommandPayload): Promise<void> {
    const raw = (payload?.diff ?? '').trim();
    if (!raw) {
      vscode.window.showWarningMessage('No diff payload was provided.');
      return;
    }

    const unifiedDiff = this.extractUnifiedDiff(raw);
    const patches = this.parseUnifiedDiff(unifiedDiff);
    if (patches.length === 0) {
      vscode.window.showWarningMessage('Unable to parse diff payload into file patches.');
      return;
    }

    const files = await this.buildPendingFiles(patches, payload?.sessionId);
    if (files.length === 0) {
      vscode.window.showWarningMessage('No applicable file changes were found in the diff payload.');
      return;
    }

    this.pending = {
      sessionId: payload?.sessionId,
      source: payload?.source ?? 'unknown',
      createdAt: Date.now(),
      rawDiff: raw,
      files
    };

    await this.openPreview(this.pending.files[0], 1, this.pending.files.length);
    const selection = await vscode.window.showInformationMessage(
      `OpenCode staged ${this.pending.files.length} diff file(s).`,
      'Apply',
      'Reject',
      this.pending.files.length > 1 ? 'Preview All' : 'Preview'
    );

    if (selection === 'Apply') {
      await this.applyLastDiff();
      return;
    }

    if (selection === 'Reject') {
      this.rejectLastDiff();
      return;
    }

    if (selection === 'Preview All') {
      for (let index = 1; index < this.pending.files.length; index += 1) {
        await this.openPreview(this.pending.files[index], index + 1, this.pending.files.length);
      }
      return;
    }

    if (selection === 'Preview') {
      await this.openPreview(this.pending.files[0], 1, this.pending.files.length);
    }
  }

  async applyLastDiff(): Promise<void> {
    if (!this.pending) {
      vscode.window.showWarningMessage('No staged diff available.');
      return;
    }

    const pending = this.pending;
    const workspaceEdit = new vscode.WorkspaceEdit();
    const createdOrModifiedForDecorations: PendingDiffFile[] = [];

    for (const file of pending.files) {
      if (file.isDelete || file.isCreate) {
        continue;
      }
      const doc = await vscode.workspace.openTextDocument(file.uri);
      const lastLine = Math.max(doc.lineCount - 1, 0);
      const fullRange = new vscode.Range(0, 0, lastLine, doc.lineAt(lastLine).text.length);
      workspaceEdit.replace(file.uri, fullRange, file.proposedContent);
      createdOrModifiedForDecorations.push(file);
    }

    for (const file of pending.files) {
      if (!file.isCreate) {
        continue;
      }
      await vscode.workspace.fs.writeFile(file.uri, encoder.encode(file.proposedContent));
      createdOrModifiedForDecorations.push(file);
    }

    const editApplied = await vscode.workspace.applyEdit(workspaceEdit);
    if (!editApplied) {
      vscode.window.showErrorMessage('Failed to apply staged workspace edits.');
      return;
    }

    for (const file of pending.files) {
      if (!file.isDelete) {
        continue;
      }
      const choice = await vscode.window.showWarningMessage(
        `Delete file from staged diff: ${file.path}?`,
        { modal: true },
        'Delete',
        'Keep'
      );
      if (choice !== 'Delete') {
        continue;
      }
      await vscode.workspace.fs.delete(file.uri, { useTrash: true });
    }

    this.setDecorations(createdOrModifiedForDecorations);
    this.clearPending(false);
    vscode.window.showInformationMessage('OpenCode diff applied.');
  }

  rejectLastDiff(): void {
    if (!this.pending) {
      vscode.window.showWarningMessage('No staged diff available.');
      return;
    }
    this.clearPending(true);
    vscode.window.showInformationMessage('OpenCode staged diff discarded.');
  }

  clearDecorations(): void {
    this.decorationRanges.clear();
    if (this.clearTimer) {
      clearTimeout(this.clearTimer);
      this.clearTimer = undefined;
    }
    for (const editor of vscode.window.visibleTextEditors) {
      editor.setDecorations(this.decorationType, []);
    }
  }

  private clearPending(clearDecorations: boolean): void {
    this.pending = undefined;
    if (clearDecorations) {
      this.clearDecorations();
    }
  }

  private async openPreview(file: PendingDiffFile, index: number, total: number): Promise<void> {
    const leftDoc = await vscode.workspace.openTextDocument({ content: file.originalSnapshot });
    const rightDoc = await vscode.workspace.openTextDocument({ content: file.proposedContent });
    const title = `OpenCode Diff ${index}/${total}: ${file.path}`;
    await vscode.commands.executeCommand('vscode.diff', leftDoc.uri, rightDoc.uri, title);
  }

  private async buildPendingFiles(patches: FilePatch[], sessionId?: string): Promise<PendingDiffFile[]> {
    const workspacePath = this.resolveWorkspacePath(sessionId) ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    const files: PendingDiffFile[] = [];

    for (const patch of patches) {
      const candidatePath = patch.isDelete ? patch.oldPath : patch.newPath || patch.oldPath;
      const normalizedPath = this.normalizePatchPath(candidatePath);
      if (!normalizedPath) {
        continue;
      }

      const uri = this.resolveFileUri(normalizedPath, workspacePath);
      const originalSnapshot = await this.loadOriginalSnapshot(uri, patch.isCreate);
      const proposedContent = patch.isDelete ? '' : this.applyPatchToContent(originalSnapshot, patch.hunks);
      const ranges = patch.isDelete ? [] : this.hunksToRanges(patch.hunks, proposedContent);

      files.push({
        path: normalizedPath,
        uri,
        originalSnapshot,
        proposedContent,
        isCreate: patch.isCreate,
        isDelete: patch.isDelete,
        modifiedRanges: ranges
      });
    }

    return files;
  }

  private async loadOriginalSnapshot(uri: vscode.Uri, allowMissing: boolean): Promise<string> {
    try {
      const bytes = await vscode.workspace.fs.readFile(uri);
      return decoder.decode(bytes);
    } catch (error) {
      if (allowMissing) {
        return '';
      }
      throw error;
    }
  }

  private resolveFileUri(filePath: string, workspacePath?: string): vscode.Uri {
    if (path.isAbsolute(filePath)) {
      return vscode.Uri.file(filePath);
    }

    if (workspacePath) {
      return vscode.Uri.file(path.join(workspacePath, filePath));
    }

    const root = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (root) {
      return vscode.Uri.file(path.join(root, filePath));
    }

    return vscode.Uri.file(filePath);
  }

  private extractUnifiedDiff(input: string): string {
    const fenced = Array.from(input.matchAll(/```diff\n([\s\S]*?)```/g)).map((match) => match[1]);
    if (fenced.length > 0) {
      return fenced.join('\n');
    }
    return input;
  }

  private parseUnifiedDiff(diff: string): FilePatch[] {
    const lines = diff.replace(/\r\n/g, '\n').split('\n');
    const patches: FilePatch[] = [];
    let current: FilePatch | undefined;
    let i = 0;

    const flushCurrent = () => {
      if (!current) {
        return;
      }
      if (current.oldPath === '/dev/null') {
        current.isCreate = true;
      }
      if (current.newPath === '/dev/null') {
        current.isDelete = true;
      }
      patches.push(current);
      current = undefined;
    };

    while (i < lines.length) {
      const line = lines[i];
      if (line.startsWith('diff --git ')) {
        flushCurrent();
        const parts = line.trim().split(/\s+/);
        current = {
          oldPath: this.stripGitPrefix(parts[2] ?? ''),
          newPath: this.stripGitPrefix(parts[3] ?? ''),
          isCreate: false,
          isDelete: false,
          hunks: []
        };
        i += 1;
        continue;
      }

      if (line.startsWith('--- ')) {
        if (!current) {
          current = { oldPath: '', newPath: '', isCreate: false, isDelete: false, hunks: [] };
        }
        current.oldPath = this.parseDiffHeaderPath(line.slice(4));
        i += 1;
        continue;
      }

      if (line.startsWith('+++ ')) {
        if (!current) {
          current = { oldPath: '', newPath: '', isCreate: false, isDelete: false, hunks: [] };
        }
        current.newPath = this.parseDiffHeaderPath(line.slice(4));
        i += 1;
        continue;
      }

      if (line.startsWith('new file mode')) {
        if (!current) {
          current = { oldPath: '', newPath: '', isCreate: false, isDelete: false, hunks: [] };
        }
        current.isCreate = true;
        i += 1;
        continue;
      }

      if (line.startsWith('deleted file mode')) {
        if (!current) {
          current = { oldPath: '', newPath: '', isCreate: false, isDelete: false, hunks: [] };
        }
        current.isDelete = true;
        i += 1;
        continue;
      }

      if (line.startsWith('@@ ')) {
        if (!current) {
          current = { oldPath: '', newPath: '', isCreate: false, isDelete: false, hunks: [] };
        }

        const parsed = this.parseHunkHeader(line);
        if (!parsed) {
          i += 1;
          continue;
        }

        i += 1;
        const hunkLines: string[] = [];
        while (i < lines.length) {
          const next = lines[i];
          if (next.startsWith('diff --git ') || next.startsWith('@@ ')) {
            break;
          }
          if (next.startsWith('--- ') && !hunkLines.some((entry) => entry.startsWith('+') || entry.startsWith('-') || entry.startsWith(' '))) {
            break;
          }
          const prefix = next.slice(0, 1);
          if (prefix === ' ' || prefix === '+' || prefix === '-' || prefix === '\\') {
            hunkLines.push(next);
          }
          i += 1;
        }

        current.hunks.push({ ...parsed, lines: hunkLines });
        continue;
      }

      i += 1;
    }

    flushCurrent();
    return patches.filter((patch) => (patch.oldPath || patch.newPath) && patch.hunks.length > 0);
  }

  private parseHunkHeader(line: string): Omit<Hunk, 'lines'> | undefined {
    const match = line.match(/^@@\s+-(\d+)(?:,(\d+))?\s+\+(\d+)(?:,(\d+))?\s+@@/);
    if (!match) {
      return undefined;
    }

    return {
      oldStart: Number.parseInt(match[1], 10),
      oldCount: Number.parseInt(match[2] ?? '1', 10),
      newStart: Number.parseInt(match[3], 10),
      newCount: Number.parseInt(match[4] ?? '1', 10)
    };
  }

  private applyPatchToContent(original: string, hunks: Hunk[]): string {
    const sourceLines = original.split('\n');
    const output: string[] = [];
    let sourceIndex = 0;

    for (const hunk of hunks) {
      const targetSourceIndex = Math.max(hunk.oldStart - 1, 0);
      if (targetSourceIndex > sourceLines.length) {
        throw new Error('Diff hunk exceeds source line count.');
      }

      while (sourceIndex < targetSourceIndex) {
        output.push(sourceLines[sourceIndex]);
        sourceIndex += 1;
      }

      for (const line of hunk.lines) {
        const marker = line.slice(0, 1);
        const value = line.slice(1);
        if (marker === ' ') {
          output.push(sourceLines[sourceIndex] ?? value);
          sourceIndex += 1;
          continue;
        }
        if (marker === '-') {
          sourceIndex += 1;
          continue;
        }
        if (marker === '+') {
          output.push(value);
          continue;
        }
      }
    }

    while (sourceIndex < sourceLines.length) {
      output.push(sourceLines[sourceIndex]);
      sourceIndex += 1;
    }

    return output.join('\n');
  }

  private hunksToRanges(hunks: Hunk[], proposedContent: string): LineRange[] {
    const maxLine = Math.max(proposedContent.split('\n').length, 1);
    const ranges: LineRange[] = [];
    for (const hunk of hunks) {
      const startLine = Math.min(Math.max(hunk.newStart, 1), maxLine);
      const span = Math.max(hunk.newCount, 1);
      const endLine = Math.min(startLine + span - 1, maxLine);
      ranges.push({ startLine, endLine });
    }
    return ranges;
  }

  private setDecorations(files: PendingDiffFile[]): void {
    this.decorationRanges.clear();
    for (const file of files) {
      if (file.modifiedRanges.length === 0) {
        continue;
      }
      const ranges = file.modifiedRanges.map((range) => {
        const start = Math.max(range.startLine - 1, 0);
        const end = Math.max(range.endLine - 1, start);
        return new vscode.Range(start, 0, end, Number.MAX_SAFE_INTEGER);
      });
      this.decorationRanges.set(file.uri.toString(), ranges);
    }

    this.applyDecorationsToVisibleEditors();

    if (this.clearTimer) {
      clearTimeout(this.clearTimer);
    }
    this.clearTimer = setTimeout(() => {
      this.clearDecorations();
    }, 30000);
  }

  private applyDecorationsToVisibleEditors(): void {
    for (const editor of vscode.window.visibleTextEditors) {
      const ranges = this.decorationRanges.get(editor.document.uri.toString()) ?? [];
      editor.setDecorations(this.decorationType, ranges);
    }
  }

  private normalizePatchPath(value: string): string {
    const trimmed = value.trim();
    if (!trimmed || trimmed === '/dev/null') {
      return '';
    }
    return this.stripGitPrefix(trimmed);
  }

  private parseDiffHeaderPath(value: string): string {
    const first = value.trim().split(/\s+/)[0] ?? '';
    return this.stripGitPrefix(first);
  }

  private stripGitPrefix(value: string): string {
    if (value === '/dev/null') {
      return value;
    }
    if (value.startsWith('a/') || value.startsWith('b/')) {
      return value.slice(2);
    }
    return value;
  }
}
