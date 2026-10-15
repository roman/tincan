_inputs: { pkgs, ... }:
pkgs.buildGoModule rec {
  pname = "tincan-command";
  version = pkgs.lib.trim (builtins.readFile ../../VERSION);
  src = import ../source.nix { inherit (pkgs) lib; };
  go = pkgs.go_1_25;
  vendorHash = "sha256-Fx5aHPDPjwe9iomWBJa3yMcuIHx4W2CtHwMg1q62rDI=";
  subPackages = [ "cmd/tincan" ];
  ldflags = [ "-X github.com/roman/tincan/pkg/tincancli.version=${version}" ];
  checkPhase = ''
    runHook preCheck
    bash ${../tests/package-boundary.sh}
    go test ./...
    runHook postCheck
  '';
}
