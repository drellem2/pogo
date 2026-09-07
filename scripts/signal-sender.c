/*
 * signal-sender.c — run a command and, if a signal lands on THIS process,
 * record the pid of the process that SENT it (mg-cbc3).
 *
 * WHY THIS EXISTS
 *
 * mg-3bd1 measured three merge gates on this host ended by a SIGTERM the
 * refinery did not send (2026-08-19 at 178s, 2026-09-07 at 85s and 264s), and
 * closed with the sender unidentified. It recorded the reason as a dead end:
 *
 *     "darwin gives a shell no way to learn a sending pid. It is available
 *      only to a handler installed with SA_SIGINFO, which a shell cannot
 *      install and Go's os/signal does not expose. Without root there is no
 *      dtrace."
 *
 * Every clause of that is true and the conclusion does not follow. A SHELL
 * cannot install SA_SIGINFO and Go does not expose siginfo_t — but a thirty-
 * line C program installs it in one call, /usr/bin/cc is present on this box,
 * and no privilege is required. Measured here on 2026-09-07, darwin 24.6.0
 * arm64, with the sender's pid known in advance:
 *
 *     sender = the calling shell   (pid 52601)  ->  si_pid=52601 si_uid=501
 *     sender = a distinct subshell (pid 52656)  ->  si_pid=52656 si_uid=501
 *
 * si_pid named the sender exactly, both times. The fact was recoverable for
 * all three occurrences and nothing was recording it.
 *
 * WHERE IT SITS, AND WHY THERE
 *
 * The delivery shape mg-3bd1 reconstructed bounds the signalled set ABOVE by
 * scripts/tmpdir-leak-guard.sh: the guard, scripts/go-test-budget.sh and
 * `go test` were signalled; test.sh, build.sh and the gate's `sh -c` survived
 * and ran their EXIT traps. So this wrapper goes INSIDE the guard, between it
 * and the budget shell — the outermost position the recorded kills actually
 * reach. scripts/signal-witness.sh sits OUTSIDE the guard and would have taken
 * its AMBIGUOUS arm on all three; that is not a defect in it (its subject is
 * the ancestor reading, which has to be taken from outside), it is why this
 * file is a second instrument rather than an edit to that one.
 *
 * WHAT IT DOES NOT DO
 *
 *   SIGKILL       no handler runs for it, here or anywhere. Unchanged.
 *   PID REUSE     si_pid is an integer, and this box recycles the whole pid
 *                 space in minutes (~292-469 pids/s measured across the
 *                 2026-09-07T20:08Z occurrence; see the investigation). So the
 *                 `ps` block below is taken IMMEDIATELY and is still only
 *                 evidence about whoever holds that pid at that instant. The
 *                 raw si_pid is always recorded; the resolution is labelled
 *                 separately and may be empty, which is itself a reading.
 *   ATTRIBUTION   it records who sent the signal, not why.
 *
 * It is transparent: the wrapped command's exit status is passed through, a
 * caught signal is forwarded to the child and then re-raised on this process
 * so its own wait status still says it was killed. A wrapper that turned a
 * kill into an ordinary exit would destroy the evidence it exists to keep.
 *
 * USAGE
 *     signal-sender <command> [args...]
 *
 * ENVIRONMENT
 *     POGO_SIGNAL_WITNESS_DIR   directory for the record. Required; the shell
 *                               front-end (scripts/signal-sender.sh) resolves
 *                               it the way config.PogoHome does. If it is
 *                               unset or unwritable the record goes to stderr
 *                               only, and the report says so.
 *     POGO_SIGNAL_SENDER_PS     the per-pid resolver. Default
 *                               `ps -o pid=,ppid=,pgid=,user=,lstart=,args= -p`.
 *                               The suite overrides it to stay deterministic.
 *
 * EXIT STATUS
 *     the child's, unchanged; 128+N if the child died of signal N and this
 *     process was not itself signalled; 2 for a usage error; 127 if the
 *     command could not be executed.
 */

#include <errno.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define ANCESTOR_LIMIT 8

static volatile sig_atomic_t caught_signo = 0;
static volatile sig_atomic_t caught_pid = -1;
static volatile sig_atomic_t caught_uid = -1;
static volatile sig_atomic_t caught_code = -1;

/*
 * The handler does nothing but store. Everything that formats, opens files or
 * forks a `ps` happens back in the main loop, which waitpid() drops us into
 * with EINTR — none of that is async-signal-safe and a witness that deadlocks
 * inside a signal handler records nothing at all.
 */
static void on_signal(int signo, siginfo_t *info, void *ctx) {
    (void)ctx;
    if (caught_signo != 0) return; /* keep the FIRST; a later one is noise */
    if (info != NULL) {
        caught_pid = (sig_atomic_t)info->si_pid;
        caught_uid = (sig_atomic_t)info->si_uid;
        caught_code = (sig_atomic_t)info->si_code;
    }
    caught_signo = signo;
}

static const char *signame(int signo) {
    switch (signo) {
    case SIGTERM: return "SIGTERM";
    case SIGINT:  return "SIGINT";
    case SIGHUP:  return "SIGHUP";
    case SIGQUIT: return "SIGQUIT";
    default:      return "signal";
    }
}

/*
 * Append `ps` output for one pid. Returns 1 if the pid resolved to anything.
 *
 * `width` caps each line for the STDERR copy and is 0 (uncapped) for the file.
 * That split is not cosmetic and it is the same reasoning that put mg-3bd1's
 * process table in a file: the refinery PERSISTS 8 KB of gate output, head and
 * tail (internal/refinery/gateoutputcap.go), and an agent's argv on this box
 * runs to well over a kilobyte. Nine of them printed in full would evict the
 * failure text this block is attached to — the remedy exhibiting the defect it
 * remedies. The uncut lines are in the record file.
 */
static int describe_pid(FILE *out, long pid, size_t width) {
    const char *psbase = getenv("POGO_SIGNAL_SENDER_PS");
    char cmd[512];
    char line[4096];
    FILE *p;
    int wrote = 0;

    if (psbase == NULL || psbase[0] == '\0')
        psbase = "ps -o pid=,ppid=,pgid=,user=,lstart=,args= -p";
    if (snprintf(cmd, sizeof cmd, "%s %ld 2>/dev/null", psbase, pid) >= (int)sizeof cmd)
        return 0;
    p = popen(cmd, "r");
    if (p == NULL) return 0;
    while (fgets(line, sizeof line, p) != NULL) {
        size_t n = strlen(line);
        while (n > 0 && (line[n - 1] == '\n' || line[n - 1] == '\r')) line[--n] = '\0';
        if (width > 0 && n > width) {
            line[width] = '\0';
            fprintf(out, "      %s ...[%lu more chars; full line in the record file]\n",
                    line, (unsigned long)(n - width));
        } else {
            fprintf(out, "      %s\n", line);
        }
        wrote = 1;
    }
    pclose(p);
    return wrote;
}

/* Read one pid's ppid, or -1. */
static long ppid_of(long pid) {
    char cmd[128];
    char line[128];
    FILE *p;
    long v = -1;

    if (snprintf(cmd, sizeof cmd, "ps -o ppid= -p %ld 2>/dev/null", pid) >= (int)sizeof cmd)
        return -1;
    p = popen(cmd, "r");
    if (p == NULL) return -1;
    if (fgets(line, sizeof line, p) != NULL) v = strtol(line, NULL, 10);
    pclose(p);
    return v;
}

static void report(FILE *out, int signo, long spid, long suid, long scode,
                   pid_t self, pid_t child, const char *path, size_t width) {
    long p;
    int depth;
    int resolved;

    fprintf(out, "\n");
    fprintf(out, "=============================================================================\n");
    fprintf(out, "SIGNAL SENDER — %s was DELIVERED to this process (mg-cbc3)\n", signame(signo));
    fprintf(out, "\n");
    fprintf(out, "  wrapper pid %ld, its parent %ld, wrapped child %ld\n",
            (long)self, (long)getppid(), (long)child);
    fprintf(out, "  caught with SA_SIGINFO, so the sending pid below is a MEASUREMENT and\n");
    fprintf(out, "  not an inference from an exit status.\n");
    fprintf(out, "\n");
    if (spid > 0) {
        fprintf(out, "  SENDER pid=%ld uid=%ld si_code=%ld\n", spid, suid, scode);
        fprintf(out, "\n");
        fprintf(out, "  The sender AND ITS ANCESTORS, resolved now — the pid alone is an integer,\n");
        fprintf(out, "  and this host recycles the whole pid space in minutes, so a pid resolved\n");
        fprintf(out, "  later names a different process or nothing at all:\n");
        resolved = describe_pid(out, spid, width);
        p = spid;
        for (depth = 0; depth < ANCESTOR_LIMIT; depth++) {
            p = ppid_of(p);
            if (p <= 1) break;
            describe_pid(out, p, width);
        }
        if (!resolved) {
            fprintf(out, "      (pid %ld resolved to nothing — the sender had already exited.\n", spid);
            fprintf(out, "       The pid above is still the measurement; this line is the reading\n");
            fprintf(out, "       that failed, and a short-lived sender is itself information.)\n");
        }
    } else {
        fprintf(out, "  SENDER pid=%ld (si_code=%ld) — NOT a userspace kill(2) with an\n", spid, scode);
        fprintf(out, "  identifiable sender. si_pid is 0 or absent for kernel-originated\n");
        fprintf(out, "  signals. Nothing here names a process, and it must not be read as if\n");
        fprintf(out, "  it did.\n");
    }
    fprintf(out, "\n");
    if (path != NULL && path[0] != '\0')
        fprintf(out, "  Also written to: %s\n", path);
    else
        fprintf(out, "  NOT written to a file — no writable POGO_SIGNAL_WITNESS_DIR. This block\n"
                     "  on stderr is the only copy, and the refinery worktree it is running in\n"
                     "  is deleted when the merge request resolves.\n");
    fprintf(out, "\n");
    fprintf(out, "  A run that was signalled did not finish asserting anything: this is not a\n");
    fprintf(out, "  verdict on the branch.\n");
    fprintf(out, "=============================================================================\n");
    fprintf(out, "\n");
}

int main(int argc, char **argv) {
    struct sigaction sa;
    pid_t child;
    int status = 0;
    int signos[4];
    size_t i;
    const char *dir;
    char path[1024];
    char stamp[32];
    time_t now;
    struct tm tmv;
    FILE *f;
    int reported = 0;

    if (argc < 2) {
        fprintf(stderr, "usage: signal-sender <command> [args...]\n");
        return 2;
    }

    signos[0] = SIGTERM;
    signos[1] = SIGINT;
    signos[2] = SIGHUP;
    signos[3] = SIGQUIT;

    memset(&sa, 0, sizeof sa);
    sa.sa_sigaction = on_signal;
    sa.sa_flags = SA_SIGINFO; /* deliberately NOT SA_RESTART: waitpid must
                               * return EINTR so the report runs promptly. */
    sigemptyset(&sa.sa_mask);
    for (i = 0; i < sizeof signos / sizeof signos[0]; i++) {
        if (sigaction(signos[i], &sa, NULL) != 0) {
            /* An instrument that cannot arm must not take the gate with it. */
            fprintf(stderr, "signal-sender: sigaction(%s): %s — running unwitnessed\n",
                    signame(signos[i]), strerror(errno));
        }
    }

    child = fork();
    if (child < 0) {
        fprintf(stderr, "signal-sender: fork: %s\n", strerror(errno));
        return 127;
    }
    if (child == 0) {
        /* Default dispositions in the child: the wrapper's handlers are an
         * instrument, and inheriting them would change what the wrapped
         * command does under a signal. Same process group, deliberately —
         * the delivery shape this exists to measure is a property of the
         * gate's group topology and must not be altered by measuring it. */
        for (i = 0; i < sizeof signos / sizeof signos[0]; i++)
            signal(signos[i], SIG_DFL);
        execvp(argv[1], &argv[1]);
        fprintf(stderr, "signal-sender: %s: %s\n", argv[1], strerror(errno));
        _exit(127);
    }

    for (;;) {
        pid_t w = waitpid(child, &status, 0);
        if (w == child) break;
        if (w < 0 && errno != EINTR) break;
        if (caught_signo != 0 && !reported) {
            int signo = (int)caught_signo;
            long spid = (long)caught_pid;
            long suid = (long)caught_uid;
            long scode = (long)caught_code;
            reported = 1;
            path[0] = '\0';
            dir = getenv("POGO_SIGNAL_WITNESS_DIR");
            if (dir != NULL && dir[0] != '\0') {
                now = time(NULL);
                gmtime_r(&now, &tmv);
                strftime(stamp, sizeof stamp, "%Y%m%dT%H%M%SZ", &tmv);
                if (snprintf(path, sizeof path, "%s/%s.%ld.sender.txt",
                             dir, stamp, (long)getpid()) >= (int)sizeof path)
                    path[0] = '\0';
                if (path[0] != '\0') {
                    f = fopen(path, "w");
                    if (f == NULL) {
                        path[0] = '\0';
                    } else {
                        report(f, signo, spid, suid, scode, getpid(), child, path, 0);
                        fclose(f);
                    }
                }
            }
            /* 200 columns on stderr: enough to identify a process, small
             * enough that nine of them cannot displace the gate's failure
             * text under the refinery's persisted-output cap. */
            report(stderr, signo, spid, suid, scode, getpid(), child, path, 200);
            fflush(stderr);
            /* Forward, so the wrapped command sees what it would have seen
             * had this wrapper not been in the chain. */
            kill(child, signo);
        }
    }

    if (caught_signo != 0) {
        /* Die of the signal rather than of a chosen status, so this process's
         * own wait status still says what happened to it. */
        int signo = (int)caught_signo;
        signal(signo, SIG_DFL);
        raise(signo);
    }
    if (WIFSIGNALED(status)) return 128 + WTERMSIG(status);
    return WEXITSTATUS(status);
}
