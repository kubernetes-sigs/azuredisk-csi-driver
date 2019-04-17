# v0.2.0-alpha

## [v0.2.0-alpha](https://github.com/kubernetes-sigs/azuredisk-csi-driver/tree/v0.2.0-alpha) (2019-02-12)

[Documentation](https://github.com/kubernetes-sigs/azuredisk-csi-driver/blob/v0.2.0-alpha/README.md)

**Downloads for v0.2.0**

filename  | sha512 hash
--------- | ------------
[v0.2.0-alpha.zip](https://github.com/kubernetes-sigs/azuredisk-csi-driver/archive/v0.2.0-alpha.zip) | `f80cdda1b2a882bbb8104202b6af84ac840119f6ccf60b8d468a6ce58e3e7bea361731c08489a2440ac4194f2c201e78123217125e510645c66e139cca7ad749`
[v0.2.0-alpha.tar.gz](https://github.com/kubernetes-sigs/azuredisk-csi-driver/archive/v0.2.0-alpha.tar.gz) | `dadb4851261cf8329844661b93a6ee1af89d6133ae1452090bff115a5c334ff05843d04c9b5e9820d3e4369e75b57ed22f818ef154fdb12adfa6734c863fd5a0`

**Closed issues:**

- Cannot mount disk due to findDiskByLun\(0\) failed within timeout [\#73](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/73)
- Only warning and error logs on azuredisk driver [\#68](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/68)
- switch azure cloud provider to 1.14 [\#67](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/67)
- check whether this driver could run on a RBAC enabled k8s cluster [\#66](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/66)
- Add boilerplate checking [\#59](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/59)
- reduce vendor size [\#58](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/58)
- add "dep ensure" check in CI test [\#54](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/54)
- \[code refactor\] switch glog to klog [\#41](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/41)
- \[management\] set up the CLI bot [\#40](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/40)
- \[management \] set up CLA check bot [\#36](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/36)
- \[management\] set up PR template [\#35](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/35)
- \[management\] Add CHANGELOG for v0.1.0-alpha release [\#34](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/34)
- \[feature\] k8s liveness probe [\#31](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/31)
- \[feature\] support MaxVolumesPerNode [\#30](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/30)
- \[feature\] Create Helm chart for driver [\#28](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/28)
- \[management\] set up issue template [\#26](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/26)
- \[test\] set up sanity test [\#9](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/9)
- write some unit tests [\#7](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/7)

**Merged pull requests:**

- fix error when 2 pods mount same disk pvc on the same node [\#75](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/75) ([andyzhangx](https://github.com/andyzhangx))
- add boilerplate check [\#74](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/74) ([tall49er](https://github.com/tall49er))
- fix DeleteVolume sanity test for invalid volumeId [\#71](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/71) ([Masquerade0097](https://github.com/Masquerade0097))
- enhance\(version\): Add version flag in azuredisk plugin [\#70](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/70) ([ashishranjan738](https://github.com/ashishranjan738))
- fix\(klog\): fix info values not getting printed with klog [\#69](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/69) ([ashishranjan738](https://github.com/ashishranjan738))
- enhance\(dep\): adds dep check in travis [\#65](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/65) ([ashishranjan738](https://github.com/ashishranjan738))
- enhance\(dep\): adds prune options in gopkg.toml [\#64](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/64) ([ashishranjan738](https://github.com/ashishranjan738))
- Fail ControllerPublishVolume when volume doesn't exist [\#62](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/62) ([Masquerade0097](https://github.com/Masquerade0097))
- Fail ControllerPublishVolume when no node exists [\#61](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/61) ([Masquerade0097](https://github.com/Masquerade0097))
- Adds MaxVolumesPerNode info in NodeGetInfoResponse [\#60](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/60) ([ashishranjan738](https://github.com/ashishranjan738))
- add OWNERS [\#57](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/57) ([andyzhangx](https://github.com/andyzhangx))
- Fail when requested volume does not exist [\#56](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/56) ([Masquerade0097](https://github.com/Masquerade0097))
- refactor: Changes of  dep ensure -update [\#53](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/53) ([ashishranjan738](https://github.com/ashishranjan738))
- fix deploy yaml files [\#52](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/52) ([lizebang](https://github.com/lizebang))
- \[WIP\] \[feature\] Create Helm chart for driver [\#51](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/51) ([lizebang](https://github.com/lizebang))
- refactor: tunes livenessprobe specs for csi drivers [\#50](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/50) ([ashishranjan738](https://github.com/ashishranjan738))
- refactor: refactor to use klog from glog [\#48](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/48) ([ashishranjan738](https://github.com/ashishranjan738))
- \[WIP\] enhance: adds csi liveness check for azure csi driver [\#47](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/47) ([ashishranjan738](https://github.com/ashishranjan738))
- Add Pull Request Template [\#39](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/39) ([Masquerade0097](https://github.com/Masquerade0097))
- Add Issue Templates [\#37](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/37) ([Masquerade0097](https://github.com/Masquerade0097))
- Move to use CSI 1.0.0 [\#22](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/22) ([andyzhangx](https://github.com/andyzhangx))
- Enable Sanity Testing [\#21](https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/21) ([Masquerade0097](https://github.com/Masquerade0097))
