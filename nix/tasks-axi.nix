{ lib, stdenvNoCC, cacert, makeWrapper, nodejs }:

let
  version = "0.2.6";
  modules = stdenvNoCC.mkDerivation {
    pname = "tasks-axi-node-modules";
    inherit version;
    nativeBuildInputs = [ nodejs ];
    dontUnpack = true;
    dontFixup = true;
    installPhase = ''
      runHook preInstall
      export HOME="$TMPDIR"
      export npm_config_cafile=${cacert}/etc/ssl/certs/ca-bundle.crt
      npm install --prefix "$out" --ignore-scripts \
        --no-audit --no-fund --no-package-lock --no-progress \
        --before=2026-09-26T06:00:00Z \
        "tasks-axi@${version}"
      runHook postInstall
    '';
    outputHashMode = "recursive";
    outputHashAlgo = "sha256";
    outputHash = "sha256-gaQ8adGjxzQKvqnUNd3Yj4z+TC1dE/e3v/UvZHZb8ls=";
  };
in
stdenvNoCC.mkDerivation {
  pname = "tasks-axi";
  inherit version;
  nativeBuildInputs = [ makeWrapper ];
  dontUnpack = true;
  installPhase = ''
    runHook preInstall
    makeWrapper ${lib.getExe nodejs} "$out/bin/tasks-axi" \
      --add-flags "${modules}/node_modules/tasks-axi/dist/bin/tasks-axi.js"
    runHook postInstall
  '';
  meta = {
    homepage = "https://github.com/kunchenguid/tasks-axi";
    license = lib.licenses.mit;
    mainProgram = "tasks-axi";
    platforms = nodejs.meta.platforms;
  };
}
