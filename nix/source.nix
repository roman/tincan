{ lib }:
lib.fileset.toSource {
  root = ../.;
  fileset = lib.fileset.unions [ ../go.mod ../go.sum ../cmd ../pkg ];
}
