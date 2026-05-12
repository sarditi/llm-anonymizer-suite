import * as vscode from 'vscode';
import { SessionStore } from './sessions';
import { SidebarProvider } from './sidebarProvider';
import { addCodebaseDir, pickDirectoryAndStore } from './settings';

export function activate(ctx: vscode.ExtensionContext): void {
  const store = new SessionStore(ctx);
  const provider = new SidebarProvider(ctx, store);

  ctx.subscriptions.push(
    vscode.window.registerWebviewViewProvider(SidebarProvider.viewType, provider, {
      webviewOptions: { retainContextWhenHidden: true },
    }),
  );

  ctx.subscriptions.push(
    vscode.commands.registerCommand('poorMansClaude.openSettings', () => {
      void vscode.commands.executeCommand('workbench.action.openSettings', 'poorMansClaude');
    }),
    vscode.commands.registerCommand('poorMansClaude.pickUploadDir', async () => {
      const r = await pickDirectoryAndStore('uploadDir', 'Upload directory', true);
      if (r) vscode.window.showInformationMessage(`Upload directory set to ${r}`);
      provider.refresh();
    }),
    vscode.commands.registerCommand('poorMansClaude.pickDownloadDir', async () => {
      const r = await pickDirectoryAndStore('downloadDir', 'Download directory', true);
      if (r) vscode.window.showInformationMessage(`Download directory set to ${r}`);
      provider.refresh();
    }),
    vscode.commands.registerCommand('poorMansClaude.addCodebaseDir', async () => {
      const r = await addCodebaseDir();
      if (r) vscode.window.showInformationMessage(`Added codebase dir ${r}`);
      provider.refresh();
    }),
  );
}

export function deactivate(): void {
  // no-op
}
