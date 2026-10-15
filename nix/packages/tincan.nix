inputs: { pkgs, ... }:
let
  command = pkgs.callPackage (import ./tincan-command.nix inputs) { };
  herdr = inputs.herdr.packages.${pkgs.stdenv.hostPlatform.system}.default;
in
pkgs.runCommand "tincan-${command.version}" {
  nativeBuildInputs = [ pkgs.makeWrapper ];
} ''
  install -Dm755 ${command}/bin/tincan "$out/bin/tincan"
  wrapProgram "$out/bin/tincan" --prefix PATH : ${herdr}/bin
''
