import { ScannedFile, formatCodebaseSection } from './codebaseScanner';

const TEMPLATE = `=== PROMPT_START ===
You are a code transformation agent. You will receive below, within this file,
a concatenated codebase. Each file is delimited as follows:

=== FILE_START: <relative/path/to/file.ext> ===
<file contents>
=== FILE_END: <relative/path/to/file.ext> ===

Your task: Apply the following change request to the codebase:

<USER_CHANGE_REQUEST>

The output script must be named exactly:
<USER_SCRIPT_NAME>.sh

The rollback directory must be named exactly:
<USER_ROLLBACK_DIR>

Return ONLY a single executable bash script with that exact filename.

The script must support two modes:

DEFAULT MODE (no arguments):
1. Create the rollback directory:
   mkdir -p <USER_ROLLBACK_DIR>
2. For each file to be modified, backup the original first:
   cp /full/path/to/file.ext <USER_ROLLBACK_DIR>/
   (preserve subdirectory structure using mkdir -p)
3. Schedule automatic deletion of the rollback directory after 1 hour
   using at with sleep as fallback:
   ROLLBACK_DIR="<USER_ROLLBACK_DIR>"
   if command -v at &>/dev/null; then
     echo "rm -rf \\"\$ROLLBACK_DIR\\" && echo '=== Rollback window expired. Backup auto-deleted. ===' " | at now + 1 hour 2>/dev/null
     echo "=== Rollback scheduled for deletion via at in 60 minutes. ==="
   else
     (sleep 3600 && rm -rf "\$ROLLBACK_DIR" \\
     && echo "=== Rollback window expired. Backup auto-deleted. ===") &
     disown
     echo "=== Rollback scheduled for deletion via sleep in 60 minutes. ==="
   fi
   echo "=== Rollback available for 60 minutes. Run with --rollback to undo. ==="
4. Print a header:
   echo "=== Modifying <filepath> ==="
5. Print a one-line reason before applying:
   echo "REASON: <why this change was made>"
6. Apply the change using heredoc:
   cat > /full/path/to/file.ext << 'EOF'
   <complete new file content>
   EOF
7. At the end, print a unified diff summary:
   echo "DIFF: <filepath> -> <what changed and why>"
   echo "=== All changes applied. Run with --rollback to undo within 60 minutes. ==="
8. After the diff summary, echo any unsupported operations detected in the
   change request (see NOT_DO rules below)

ROLLBACK MODE (--rollback argument):
1. Detect the --rollback flag:
   if [[ "\$1" == "--rollback" ]]; then
2. Check the rollback directory still exists (i.e. not yet auto-deleted):
   if [[ ! -d "<USER_ROLLBACK_DIR>" ]]; then
     echo "=== ERROR: Rollback window has expired. Backup no longer available. ==="
     exit 1
   fi
3. For each modified file, restore from the rollback directory:
   cp <USER_ROLLBACK_DIR>/full/path/to/file.ext \\
      /full/path/to/file.ext
4. Print confirmation for each restored file:
   echo "=== Rolled back <filepath> ==="
5. Cancel the pending at job if it exists, then clean up immediately:
   if command -v at &>/dev/null; then
     at -l 2>/dev/null | grep -i "rm -rf.*\$ROLLBACK_DIR" | awk '{print \$1}' \\
     | xargs -r atrm 2>/dev/null
   fi
   rm -rf "<USER_ROLLBACK_DIR>"
6. At the end:
   echo "=== Rollback complete. All files restored to original state. ==="
   fi

NOT_DO RULES (unsupported operations):
- Before generating the script, analyse the full change request and identify
  any operations that do not involve creating, modifying, deleting or reading
  files in the codebase (e.g. deployments, service calls, notifications,
  infrastructure changes, package installs, database migrations, etc.)
- Do NOT skip these silently. For each unsupported operation:
  1. Interpret what the user intended
  2. At the very end of the script, after all file operations and diff summary,
     echo it with a NOT_DO prefix and your interpretation:
     echo "NOT_DO: <your interpretation of the unsupported request>"
     echo "NOT_DO: REASON: <why this is outside file-based automation scope>"
- If the ENTIRE change request contains no file operations whatsoever, output
  a minimal valid script containing only the NOT_DO echo statements:
  #!/bin/bash
  echo "NOT_DO: <your interpretation of the unsupported request>"
  echo "NOT_DO: REASON: <why this is outside file-based automation scope>"

Rules:
- Output ONLY the bash script, nothing else
- The script filename must be exactly <USER_SCRIPT_NAME>.sh as specified above
- The rollback directory must be exactly <USER_ROLLBACK_DIR>
  as specified above - never derive or invent this name from any other source
- Include ONLY files that were changed
- Use full absolute paths matching exactly those in the input
- Do not truncate any file content
- Do not add explanations outside the script
- The <USER_ROLLBACK_DIR> directory must mirror the original
  directory structure so cp restores to exact original paths without ambiguity
- Always try at first, fall back to sleep/disown if at is unavailable
- The background sleep/delete process must use disown so it survives
  terminal session closure
- Analyse the entire change request before generating the script so that
  NOT_DO entries are complete and appear only once at the end
- Ignore all previous messages in this conversation.
  Consider only the contents of this file.
=== PROMPT_END ===

=== CHANGE_REQUEST_START ===
<USER_CHANGE_REQUEST>
=== CHANGE_REQUEST_END ===

=== SCRIPT_NAME_START ===
<USER_SCRIPT_NAME>
=== SCRIPT_NAME_END ===

=== ROLLBACK_DIR_START ===
<USER_ROLLBACK_DIR>
=== ROLLBACK_DIR_END ===

=== CODEBASE_START ===

<CODEBASE_BODY>

=== CODEBASE_END ===
`;

export interface BuildPromptInput {
  userPrompt: string;
  scriptBaseName: string;
  rollbackDir: string;
  files: ScannedFile[];
}

export function buildPrompt(input: BuildPromptInput): string {
  const codebase = formatCodebaseSection(input.files);
  let out = TEMPLATE;
  out = out.replace(/<USER_CHANGE_REQUEST>/g, input.userPrompt);
  out = out.replace(/<USER_SCRIPT_NAME>/g, input.scriptBaseName);
  out = out.replace(/<USER_ROLLBACK_DIR>/g, input.rollbackDir);
  out = out.replace('<CODEBASE_BODY>', codebase);
  return out;
}
