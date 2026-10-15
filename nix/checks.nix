{
  perSystem = { pkgs, self', ... }:
    let
      golinesFlags = "-m 100 --no-reformat-tags --base-formatter=gofmt";
    in {
      formatter = pkgs.writeShellApplication {
        name = "tincan-format";
        runtimeInputs = [ pkgs.golines pkgs.go_1_25 ];
        text = ''
          if [ "$#" -eq 0 ]; then
            set -- .
          fi
          exec golines ${golinesFlags} -w "$@"
        '';
      };
      checks = {
        inherit (self'.packages) tincan;
        tincan-smoke = pkgs.runCommand "tincan-smoke" {
          tincan = self'.packages.tincan;
          version = pkgs.lib.trim (builtins.readFile ../VERSION);
          grep = "${pkgs.gnugrep}/bin/grep";
        } ''
          bash ${./tests/smoke.sh}
          touch "$out"
        '';
        go-format = pkgs.runCommand "tincan-go-format" {
          nativeBuildInputs = [ pkgs.golines pkgs.go_1_25 ];
          src = import ./source.nix { inherit (pkgs) lib; };
        } ''
          cd "$src"
          unformatted="$(golines ${golinesFlags} -l .)"
          if [ -n "$unformatted" ]; then
            printf 'Go files need formatting:\n%s\n' "$unformatted" >&2
            exit 1
          fi
          touch "$out"
        '';
      };
    };
}
