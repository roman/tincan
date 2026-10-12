{
  description = "Tincan communication and Concierge supervisor tools";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/release-25.11";
  inputs.herdr.url = "github:herdrdev/herdr/v0.9.3";

  outputs = { self, nixpkgs, herdr }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forEachSystem = function:
        nixpkgs.lib.genAttrs systems (system: function (import nixpkgs { inherit system; }));
      golinesFlags = "-m 100 --no-reformat-tags --base-formatter=gofmt";
      goSource = pkgs: pkgs.lib.fileset.toSource {
        root = ./.;
        fileset = pkgs.lib.fileset.unions [ ./go.mod ./go.sum ./cmd ./pkg ];
      };
      goModule = pkgs: {
        version = "0.1.0";
        src = goSource pkgs;
        go = pkgs.go_1_25;
        vendorHash = "sha256-dnZtAeufL7NxseT95FGiPCvq6oGp6eUO7/sIQEHNRa0=";
      };
      buildCommands = pkgs:
        let tasksAxi = pkgs.callPackage ./nix/tasks-axi.nix { };
        in pkgs.buildGoModule ((goModule pkgs) // {
          pname = "tincan-commands";
          subPackages = [ "cmd/..." ];
          checkPhase = ''
            runHook preCheck
            export PATH=${tasksAxi}/bin:$PATH
            go list -deps ./cmd/tincan > tincan-dependencies
            while IFS= read -r dependency; do
              case "$dependency" in
                github.com/roman/tincan/cmd/tincan|github.com/roman/tincan/pkg/tincancli|github.com/roman/tincan/pkg/inbox|github.com/roman/tincan/pkg/notify|github.com/roman/tincan/pkg/herdr) ;;
                github.com/roman/tincan/*)
                  printf 'Forbidden Tincan dependency: %s\n' "$dependency" >&2
                  exit 1
                  ;;
              esac
            done < tincan-dependencies
            go test ./...
            runHook postCheck
          '';
        });
    in
    {
      packages = forEachSystem (pkgs:
        let
          system = pkgs.stdenv.hostPlatform.system;
          herdrPackage = herdr.packages.${system}.default;
          tasksAxi = pkgs.callPackage ./nix/tasks-axi.nix { };
          allCommands = buildCommands pkgs;
          mkProgram = { name, runtime ? null }: pkgs.runCommand "${name}-0.1.0" {
            nativeBuildInputs = pkgs.lib.optionals (runtime != null) [ pkgs.makeWrapper ];
            disallowedRequisites = pkgs.lib.optionals (runtime == null) [ herdrPackage ];
          } ''
            install -Dm755 ${allCommands}/bin/${name} "$out/bin/${name}"
            ${pkgs.lib.optionalString (runtime != null) ''
              wrapProgram "$out/bin/${name}" --prefix PATH : ${runtime}/bin
            ''}
          '';
          tincan-command = mkProgram { name = "tincan"; runtime = herdrPackage; };
          tincan-inbox = mkProgram { name = "tincan-inbox"; };
          tincan-notify = mkProgram { name = "tincan-notify"; runtime = herdrPackage; };
          tincan-task-send = mkProgram { name = "tincan-task-send"; runtime = herdrPackage; };
          tincan-task-reply = mkProgram { name = "tincan-task-reply"; };
          tincan-task-receive = mkProgram { name = "tincan-task-receive"; };
          concierge-brief = mkProgram { name = "concierge-brief"; runtime = tasksAxi; };
          concierge-primary = mkProgram { name = "concierge-primary"; };
          concierge-worker = mkProgram { name = "concierge-worker"; };
          concierge-backlog = mkProgram { name = "concierge-backlog"; runtime = tasksAxi; };
          concierge-task = mkProgram { name = "concierge-task"; runtime = herdrPackage; };
          concierge-migrate = mkProgram { name = "concierge-migrate"; };
          concierge-control = mkProgram { name = "concierge-control"; runtime = herdrPackage; };
          concierge-supervise = mkProgram { name = "concierge-supervise"; runtime = herdrPackage; };
          tincan = tincan-command;
          processTools = if pkgs.stdenv.isLinux then pkgs.procps else
            pkgs.writeShellScriptBin "ps" ''
              exec /bin/ps "$@"
            '';
          conciergeRuntime = pkgs.symlinkJoin {
            name = "concierge-runtime";
            paths = [ herdrPackage processTools ];
          };
          concierge = mkProgram { name = "concierge"; runtime = conciergeRuntime; };
        in
        {
          default = tincan;
          inherit tincan concierge tincan-command tincan-inbox tincan-notify tincan-task-send tincan-task-reply tincan-task-receive concierge-brief concierge-worker concierge-backlog concierge-task concierge-migrate concierge-control concierge-supervise;
        } // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux { inherit concierge-primary; });

      formatter = forEachSystem (pkgs: pkgs.writeShellApplication {
        name = "tincan-format";
        runtimeInputs = [ pkgs.golines pkgs.go_1_25 ];
        text = ''
          if [ "$#" -eq 0 ]; then
            set -- .
          fi
          exec golines ${golinesFlags} -w "$@"
        '';
      });

      checks = forEachSystem (pkgs:
        let
          packages = self.packages.${pkgs.stdenv.hostPlatform.system};
          healthHerdr = pkgs.writeShellScript "concierge-health-herdr" ''
            case "$1 $2" in
              'pane get') printf '{"result":{"pane":{"pane_id":"p1","foreground_cwd":"%s","workspace_id":"w1","tab_id":"t1"}}}\n' "$SMOKE_WORKTREE" ;;
              'agent get') printf '%s\n' '{"result":{"agent":{"pane_id":"p1","agent_status":"working","agent":"codex"}}}' ;;
              'pane process-info') printf '{"result":{"type":"pane_process_info","process_info":{"pane_id":"p1","shell_pid":%s,"foreground_processes":[]}}}\n' "$SMOKE_SHELL_PID" ;;
              'status --json') printf '%s\n' '{"server":{"running":true}}' ;;
              *) exit 71 ;;
            esac
          '';
        in
        {
          inherit (packages) tincan-command tincan-inbox tincan-notify tincan-task-send tincan-task-reply tincan-task-receive concierge-brief concierge-worker concierge-backlog concierge-task concierge-migrate concierge-control concierge-supervise;
          tincan-smoke = pkgs.runCommand "tincan-smoke" { } ''
            unset TINCAN_HOME HERDR_SESSION HERDR_SOCKET_PATH HERDR_WORKSPACE_ID HERDR_TAB_ID HERDR_PANE_ID
            export PATH=
            test -x ${packages.tincan-command}/bin/tincan
            test "$(${packages.tincan}/bin/tincan --version)" = 0.1.0
            ${packages.tincan}/bin/tincan --root "$TMPDIR/messages" inbox send smoke --body 'Packaged message' > sent
            ${pkgs.gnugrep}/bin/grep -q '^id: ' sent
            ${packages.tincan}/bin/tincan --root "$TMPDIR/messages" inbox list smoke > pending
            ${pkgs.gnugrep}/bin/grep -q '^count: 1$' pending
            ${packages.tincan}/bin/tincan notify --help >/dev/null
            : > "$out"
          '';
          go-source = pkgs.runCommand "tincan-go-source" { src = (goModule pkgs).src; } ''
            test -e "$src/go.mod"
            test -e "$src/cmd"
            test -e "$src/pkg"
            test ! -e "$src/README.md"
            touch "$out"
          '';
          concierge-smoke = pkgs.runCommand "concierge-smoke" { } ''
            unset CONCIERGE_HOME TINCAN_HOME TINCAN_HERDR HERDR_SESSION HERDR_SOCKET_PATH HERDR_WORKSPACE_ID HERDR_TAB_ID HERDR_PANE_ID
            export PATH=
            test -x ${packages.concierge}/bin/concierge
            test "$(${packages.concierge}/bin/concierge --version)" = 0.1.0
            set -- ${packages.concierge}/bin/*
            test "$#" -eq 1
            test "$1" = ${packages.concierge}/bin/concierge
            set -- ${packages.tincan}/bin/*
            test "$#" -eq 1
            test "$1" = ${packages.tincan}/bin/tincan
            ${packages.concierge}/bin/concierge --root "$TMPDIR/state" state init > initialized
            ${pkgs.gnugrep}/bin/grep -qx 'version: 2' initialized
            ${packages.concierge}/bin/concierge --root "$TMPDIR/state" backlog add smoke-a1 'Packaged intake' \
              --kind task --repo exmachina >/dev/null
            ${packages.concierge}/bin/concierge --root "$TMPDIR/state" backlog list > backlog
            ${pkgs.gnugrep}/bin/grep -Fx '  "smoke-a1",queued,"task","exmachina","Packaged intake"' backlog
            ${packages.concierge}/bin/concierge --root "$TMPDIR/state" backlog show smoke-a1 --full > detail
            ${pkgs.gnugrep}/bin/grep -Fx '  title: "Packaged intake"' detail
            ${packages.tincan}/bin/tincan --root "$TMPDIR/messages" inbox send concierge --key wake --body 'Packaged wake' >/dev/null
            ${packages.concierge}/bin/concierge --root "$TMPDIR/state" --message-root "$TMPDIR/messages" > brief
            ${pkgs.gnugrep}/bin/grep -Fx 'wakes: 1 pending for concierge' brief
            ${pkgs.gnugrep}/bin/grep -Fx 'backlog: 1 queued' brief
            ${packages.concierge}/bin/concierge task reply --help >/dev/null
            ${packages.concierge}/bin/concierge primary --help >/dev/null
            ${packages.concierge}/bin/concierge control --help >/dev/null
            ${packages.concierge}/bin/concierge supervise --help >/dev/null

            export SMOKE_WORKTREE="$TMPDIR"
            # Health checks exclude PID 1, which Nix uses for the builder shell.
            ${pkgs.bash}/bin/bash -e <<'HEALTH'
            export SMOKE_SHELL_PID=$$
            (exec -a codex ${pkgs.coreutils}/bin/coreutils --coreutils-prog=sleep 60) &
            agent_pid=$!
            trap 'kill "$agent_pid" 2>/dev/null || true; wait "$agent_pid" 2>/dev/null || true' EXIT
            ${pkgs.jq}/bin/jq --arg worktree "$TMPDIR" \
              '.state = "in_flight" | .worker = {name:"smoke",harness:"codex",session:"smoke",workspace:"w1",tab:"t1",pane:"p1",project:$worktree,worktree:$worktree,generation:"gen-smoke",agent_args:["resume","smoke"]}' \
              "$TMPDIR/state/records/smoke-a1.json" > record
            ${pkgs.coreutils}/bin/mv record "$TMPDIR/state/records/smoke-a1.json"
            ${pkgs.coreutils}/bin/timeout --preserve-status --signal=TERM --kill-after=2s 2s \
              ${packages.concierge}/bin/concierge --root "$TMPDIR/state" --message-root "$TMPDIR/messages" \
              supervise --herdr ${healthHerdr} task smoke-a1 > supervision || {
                ${pkgs.coreutils}/bin/cat supervision >&2
                exit 1
              }
            test ! -s supervision
            HEALTH
            : > "$out"
          '';
          go-format = pkgs.runCommand "tincan-go-format" {
            nativeBuildInputs = [ pkgs.golines pkgs.go_1_25 ];
            src = goSource pkgs;
          } ''
            cd "$src"
            unformatted="$(golines ${golinesFlags} -l .)"
            if [ -n "$unformatted" ]; then
              printf 'Go files need formatting:\n%s\n' "$unformatted" >&2
              exit 1
            fi
            touch "$out"
          '';
          go-tests = buildCommands pkgs;
        } // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux { inherit (packages) concierge-primary; });

      devShells = forEachSystem (pkgs:
        let
          tasksAxi = pkgs.callPackage ./nix/tasks-axi.nix { };
        in
        {
          default = pkgs.mkShell {
            packages = [ pkgs.go_1_25 pkgs.gopls pkgs.golines tasksAxi herdr.packages.${pkgs.stdenv.hostPlatform.system}.default ];
          };
        });
    };
}
