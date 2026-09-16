{ pkgs ? import <nixpkgs> {}, allowDirty ? false }:
let
  release = builtins.fromJSON (builtins.readFile ./release.json);
  manifest = builtins.fromJSON (builtins.readFile ./web-artifact/manifest.json);
  localArchive = ./web-artifact + "/${manifest.archive.file}";
  archive = if builtins.pathExists localArchive then localArchive else pkgs.fetchurl {
    inherit (manifest.archive) url sha256;
  };
in pkgs.callPackage ./nix/package.nix {
  src = pkgs.lib.cleanSourceWith {
    src = ./.;
    filter = path: type: pkgs.lib.cleanSourceFilter path type
      && !(pkgs.lib.hasPrefix (toString ./. + "/web/dist") path)
      && !(pkgs.lib.hasPrefix (toString ./. + "/web-artifact") path)
      && builtins.baseNameOf path != "result";
  };
  inherit (release) version vendorHash;
  inherit allowDirty;
  webManifest = ./web-artifact/manifest.json;
  webArchive = archive;
}
