/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package azuredisk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	directvolume "github.com/kata-containers/kata-containers/src/runtime/pkg/direct-volume"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/mount-utils"
	"k8s.io/utils/ptr"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const kataMountInfoFile = "mountInfo.json"

// kataTestDirectVolume confines metadata operations to a test-owned root.
type kataTestDirectVolume struct{ rootPath string }

// volumeDir returns the encoded publication directory under the test root.
func (s *kataTestDirectVolume) volumeDir(target string) string {
	return filepath.Join(s.rootPath, base64.URLEncoding.EncodeToString([]byte(target)))
}

// AddMountInfo mirrors the vendor's mkdir/write (including overwrite) semantics
// without writing to the live Kata root. Lifecycle checks must prevent replacement.
func (s *kataTestDirectVolume) AddMountInfo(target string, info directvolume.MountInfo) error {
	data, err := json.Marshal(&info)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.volumeDir(target), 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.volumeDir(target), kataMountInfoFile), data, 0600)
}

// FindMountInfo scans assignments in the test root.
func (s *kataTestDirectVolume) FindMountInfo(volumeID string) (string, error) {
	return kataFindMountInfo(s.rootPath, volumeID, s.VolumeMountInfo)
}

// IsVolumeMountedByID reports a metadata assignment, not an observed guest mount.
func (s *kataTestDirectVolume) IsVolumeMountedByID(volumeID string) (bool, error) {
	target, err := s.FindMountInfo(volumeID)
	return target != "", err
}

// IsVolumeMountedByID reports a metadata assignment, not an observed guest mount.
func (f *kataStubDirectVolume) IsVolumeMountedByID(volumeID string) (bool, error) {
	target, err := f.FindMountInfo(volumeID)
	return target != "", err
}

func TestKataIsVolumeMountedByID(t *testing.T) {
	lookupErr := errors.New("inventory unavailable")
	for _, tc := range []struct {
		name   string
		target string
		err    error
	}{
		{name: "found", target: "/target"},
		{name: "absent"},
		{name: "error", err: lookupErr},
		{name: "target and error", target: "/target", err: lookupErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			store := &kataStubDirectVolume{findMountInfo: func(volumeID string) (string, error) {
				assert.Equal(t, "volume-1", volumeID)
				calls++
				return tc.target, tc.err
			}}
			mounted, err := store.IsVolumeMountedByID("volume-1")
			assert.Equal(t, tc.target != "", mounted)
			assert.Equal(t, tc.err, err)
			assert.Equal(t, 1, calls)
		})
	}
	for name, store := range map[string]kataDirectVolumer{
		"temporary root": &kataTestDirectVolume{rootPath: t.TempDir()},
		"map fake":       newFakeKataDirectVolume(),
	} {
		t.Run(name, func(t *testing.T) {
			mounted, err := store.IsVolumeMountedByID("volume-1")
			require.NoError(t, err)
			assert.False(t, mounted)
			require.NoError(t, store.AddMountInfo("/target", directvolume.MountInfo{Metadata: map[string]string{kataVolumeIDKey: "volume-1"}}))
			mounted, err = store.IsVolumeMountedByID("volume-1")
			require.NoError(t, err)
			assert.True(t, mounted)
		})
	}
	store := &kataTestDirectVolume{rootPath: filepath.Join(t.TempDir(), "missing")}
	mounted, err := store.IsVolumeMountedByID("volume-1")
	assert.False(t, mounted)
	assert.True(t, os.IsNotExist(err))
}

// VolumeMountInfo decodes the vendor-compatible metadata in the test root.
func (s *kataTestDirectVolume) VolumeMountInfo(target string) (*directvolume.MountInfo, error) {
	data, err := os.ReadFile(filepath.Join(s.volumeDir(target), kataMountInfoFile))
	if err != nil {
		return nil, err
	}
	var info directvolume.MountInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Remove deletes only the target's directory under the test root.
func (s *kataTestDirectVolume) Remove(target string) error {
	return os.RemoveAll(s.volumeDir(target))
}

// IsVolumeMounted distinguishes absent metadata from unreadable metadata.
func (s *kataTestDirectVolume) IsVolumeMounted(target string) (bool, error) {
	info, err := s.VolumeMountInfo(target)
	if os.IsNotExist(err) {
		return false, nil
	}
	return info != nil && err == nil, err
}

// newKataTestDriver builds an isolated, fake-only lifecycle fixture.
func newKataTestDriver(t *testing.T) (*Driver, *mount.FakeMounter, *mounter.FakeSafeMounter) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Kata filesystem lifecycle is Linux-only")
	}
	d := &Driver{cloud: &azure.Cloud{}, volumeLocks: volumehelper.NewVolumeLocks(),
		ioHandler: azureutils.NewFakeIOHandler(), hostUtil: azureutils.NewFakeHostUtil()}
	safeMounter, err := mounter.NewFakeSafeMounter()
	require.NoError(t, err)
	exec := safeMounter.Exec.(*mounter.FakeSafeMounter)
	// Unlike FakeSafeMounter.Mount, this fake retains mount state across RPCs.
	// No commands or mounts are allowed to escape to the real node.
	mounts := mount.NewFakeMounter(nil)
	safeMounter.Interface = mounts
	d.mounter = safeMounter
	d.enableKataMount = true
	d.kataDirectVolume = &kataTestDirectVolume{rootPath: t.TempDir()}
	d.kubeClient = fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"}, Spec: corev1.PodSpec{RuntimeClassName: ptr.To("kata")}},
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "kata", Annotations: map[string]string{kataRuntimeClassAnnotationKey: kataRuntimeClassAnnotationValue}}, Handler: "kata"},
	)
	return d, mounts, exec
}

// kataTestRequest supplies supported filesystem CSI publication inputs.
func kataTestRequest(t *testing.T) *csi.NodePublishVolumeRequest {
	t.Helper()
	return &csi.NodePublishVolumeRequest{
		VolumeId: "volume-1", StagingTargetPath: t.TempDir(), TargetPath: filepath.Join(t.TempDir(), "target"),
		PublishContext: map[string]string{consts.LUN: "1"},
		VolumeContext:  map[string]string{podNameField: "pod", podNamespaceField: "default"},
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER},
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4", MountFlags: []string{"discard"}}},
		},
	}
}

func TestKataRawBlockPublishSkipsKataWork(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(strconv.FormatBool(held), func(t *testing.T) {
			d, mounts, _ := newKataTestDriver(t)
			req := kataTestRequest(t)
			req.VolumeCapability.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
			// Any metadata inventory or other Kata adapter call would panic.
			d.kataDirectVolume = nil
			if held {
				require.True(t, d.volumeLocks.TryAcquire(req.VolumeId))
				defer d.volumeLocks.Release(req.VolumeId)
			}
			_, err := d.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			assert.Empty(t, d.kubeClient.(*fake.Clientset).Actions())
			require.Len(t, mounts.MountPoints, 1)
			assert.Equal(t, req.TargetPath, mounts.MountPoints[0].Path)
			assert.Contains(t, mounts.MountPoints[0].Opts, "bind")
			if held {
				assert.False(t, d.volumeLocks.TryAcquire(req.VolumeId), "raw block must not release an existing lifecycle lock")
			} else {
				require.True(t, d.volumeLocks.TryAcquire(req.VolumeId), "raw block must not retain a lifecycle lock")
				d.volumeLocks.Release(req.VolumeId)
			}
		})
	}
}

// kataTestStageRequest preserves the publication's immutable staging inputs.
func kataTestStageRequest(req *csi.NodePublishVolumeRequest) *csi.NodeStageVolumeRequest {
	return &csi.NodeStageVolumeRequest{
		VolumeId: req.VolumeId, StagingTargetPath: req.StagingTargetPath, PublishContext: req.PublishContext,
		VolumeCapability: req.VolumeCapability, VolumeContext: req.VolumeContext,
	}
}

// kataTestInfo builds the stable CSI metadata map for a publication.
func kataTestInfo(t *testing.T, req *csi.NodePublishVolumeRequest) directvolume.MountInfo {
	t.Helper()
	fsType, flags, err := resolveFSType(req.VolumeCapability, req.VolumeContext)
	require.NoError(t, err)
	return directvolume.MountInfo{
		VolumeType: kataDirectVolumeType, Device: "/dev/sdd", FsType: fsType,
		Options: kataMountOptions(fsType, flags, req.Readonly), Metadata: map[string]string{kataVolumeIDKey: req.VolumeId},
	}
}

func TestKataDirectVolumeStore(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Kata mounts are Linux-only")
	}
	store := &kataTestDirectVolume{rootPath: t.TempDir()}
	req := kataTestRequest(t)
	info := kataTestInfo(t, req)
	require.NoError(t, store.AddMountInfo(req.TargetPath, info))

	// A new adapter must recover the same assignment without an in-memory index.
	restarted := &kataTestDirectVolume{rootPath: store.rootPath}
	got, err := restarted.VolumeMountInfo(req.TargetPath)
	require.NoError(t, err)
	assert.Equal(t, info, *got)
	target, err := restarted.FindMountInfo(req.VolumeId)
	require.NoError(t, err)
	assert.Equal(t, req.TargetPath, target)
	files, err := os.ReadDir(store.volumeDir(req.TargetPath))
	require.NoError(t, err)
	require.Len(t, files, 1, "temporary files must not be mistaken for sandbox IDs")
	assert.Equal(t, kataMountInfoFile, files[0].Name())
	stat, err := os.Stat(filepath.Join(store.volumeDir(req.TargetPath), kataMountInfoFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), stat.Mode().Perm())

	require.NoError(t, store.AddMountInfo(req.TargetPath, info))
	changed := info
	changed.Device = "/dev/sde"
	require.NoError(t, store.AddMountInfo(req.TargetPath, changed))
	got, err = store.VolumeMountInfo(req.TargetPath)
	require.NoError(t, err)
	assert.Equal(t, changed, *got, "the vendor-compatible fake must allow overwrites")

	require.NoError(t, store.Remove(req.TargetPath))
	require.NoError(t, store.Remove(req.TargetPath))
	mounted, err := store.IsVolumeMounted(req.TargetPath)
	require.NoError(t, err)
	assert.False(t, mounted)
}

func TestKataVolumeMountInfoReaderContract(t *testing.T) {
	for _, data := range []string{"null", "{}", "", "{"} {
		t.Run(data, func(t *testing.T) {
			store := &kataTestDirectVolume{rootPath: t.TempDir()}
			target := filepath.Join(t.TempDir(), "target")
			require.NoError(t, os.Mkdir(store.volumeDir(target), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(store.volumeDir(target), kataMountInfoFile), []byte(data), 0600))
			info, err := store.VolumeMountInfo(target)
			if data == "" || data == "{" {
				require.Error(t, err)
				assert.Nil(t, info)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, info, "the vendor decodes into a struct, even for JSON null")
			assert.Equal(t, directvolume.MountInfo{}, *info)
			mounted, err := store.IsVolumeMounted(target)
			require.NoError(t, err)
			assert.True(t, mounted)
			foundTarget, err := store.FindMountInfo("volume-1")
			require.NoError(t, err)
			assert.Empty(t, foundTarget, "an empty record has no matching volume identity")
		})
	}
}

func TestKataInventoryMatchesVolumeID(t *testing.T) {
	for _, scenario := range []string{"missing device", "missing type", "foreign type with matching ID", "missing identity", "ambiguous identity", "unrelated incomplete", "foreign incomplete", "other volume same target", "read error", "empty record"} {
		t.Run(scenario, func(t *testing.T) {
			store := &kataTestDirectVolume{rootPath: t.TempDir()}
			req := kataTestRequest(t)
			info := kataTestInfo(t, req)
			target := req.TargetPath
			wantError := scenario == "read error"
			switch scenario {
			case "missing device":
				info.Device = ""
			case "missing type":
				info.VolumeType = ""
			case "foreign type with matching ID":
				info.VolumeType = "blk"
			case "missing identity":
				info.Metadata = nil
			case "ambiguous identity":
				info.Metadata, info.VolumeType = nil, ""
			case "unrelated incomplete":
				info.Metadata[kataVolumeIDKey] = "other-volume"
				info.Device, info.VolumeType = "", ""
				target += "-other"
			case "foreign incomplete":
				info.Metadata, info.VolumeType, info.Device = nil, "blk", ""
				target += "-other"
			case "other volume same target":
				info.Metadata[kataVolumeIDKey], info.Device = "other-volume", ""
			case "empty record":
				info = directvolume.MountInfo{}
			}
			require.NoError(t, store.AddMountInfo(target, info))
			read := store.VolumeMountInfo
			if scenario == "read error" {
				read = func(string) (*directvolume.MountInfo, error) {
					return nil, errors.New("unreadable metadata")
				}
			}
			gotTarget, err := kataFindMountInfo(store.rootPath, req.VolumeId, read)
			if wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				if info.Metadata[kataVolumeIDKey] == req.VolumeId {
					assert.Equal(t, target, gotTarget)
					return
				}
			}
			assert.Empty(t, gotTarget)
		})
	}
}

func TestKataInventoryFirstMatch(t *testing.T) {
	for _, scenario := range []string{"duplicate", "decode error", "read error", "empty record", "missing file", "unrelated", "foreign"} {
		for _, before := range []bool{true, false} {
			t.Run(scenario+"/before="+strconv.FormatBool(before), func(t *testing.T) {
				store := &kataTestDirectVolume{rootPath: t.TempDir()}
				req := kataTestRequest(t)
				info := kataTestInfo(t, req)
				// These encoded targets sort in the same order as their names.
				match, other := "/b", "/c"
				if before {
					other = "/a"
				}
				require.NoError(t, store.AddMountInfo(match, info))
				name := base64.URLEncoding.EncodeToString([]byte(other))
				if scenario == "decode error" {
					name = "~invalid"
					if before {
						name = "!invalid"
					}
				}
				require.NoError(t, os.Mkdir(filepath.Join(store.rootPath, name), 0700))
				var reads []string
				read := func(target string) (*directvolume.MountInfo, error) {
					reads = append(reads, target)
					if target == match {
						return &info, nil
					}
					switch scenario {
					case "read error":
						return nil, errors.New("unreadable metadata")
					case "empty record":
						return &directvolume.MountInfo{}, nil
					case "missing file":
						return nil, os.ErrNotExist
					case "unrelated":
						return &directvolume.MountInfo{Metadata: map[string]string{kataVolumeIDKey: "other-volume"}}, nil
					case "foreign":
						return &directvolume.MountInfo{VolumeType: "blk"}, nil
					default:
						return &info, nil
					}
				}
				target, err := kataFindMountInfo(store.rootPath, req.VolumeId, read)
				if before && (scenario == "decode error" || scenario == "read error") {
					require.Error(t, err)
					assert.Empty(t, target)
					assert.NotContains(t, reads, match)
					return
				}
				require.NoError(t, err)
				if before && scenario == "duplicate" {
					assert.Equal(t, other, target)
					assert.Equal(t, []string{other}, reads)
				} else {
					assert.Equal(t, match, target)
					if !before {
						assert.Equal(t, []string{match}, reads, "first match stops the scan, including later malformed entries")
					}
				}
			})
		}
	}
}

func TestKataUnstageSkipsInventory(t *testing.T) {
	d, mounts, exec := newKataTestDriver(t)
	req := kataTestRequest(t)
	d.kataDirectVolume = &kataStubDirectVolume{findMountInfo: func(string) (string, error) {
		t.Fatal("unstage must not scan metadata")
		return "", errors.New("inventory unavailable")
	}}
	require.NoError(t, mounts.Mount("/dev/sdd", req.StagingTargetPath, "ext4", nil))
	mounts.ResetLog()
	_, err := d.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{VolumeId: req.VolumeId, StagingTargetPath: req.StagingTargetPath})
	require.NoError(t, err)
	require.Len(t, mounts.GetLog(), 1)
	assert.Equal(t, mount.FakeActionUnmount, mounts.GetLog()[0].Action)
	assert.Zero(t, exec.CommandCalls)
}

func TestKataDirectVolumeInventoryErrors(t *testing.T) {
	for _, scenario := range []string{"missing root", "corrupt metadata", "empty metadata", "nonempty incomplete publication", "unfinished publication"} {
		t.Run(scenario, func(t *testing.T) {
			store := &kataTestDirectVolume{rootPath: t.TempDir()}
			req := kataTestRequest(t)
			switch scenario {
			case "missing root":
				store.rootPath = filepath.Join(store.rootPath, "missing")
			case "corrupt metadata", "empty metadata", "nonempty incomplete publication", "unfinished publication":
				require.NoError(t, os.Mkdir(store.volumeDir(req.TargetPath), 0700))
				if scenario == "corrupt metadata" {
					require.NoError(t, os.WriteFile(filepath.Join(store.volumeDir(req.TargetPath), kataMountInfoFile), []byte("{"), 0600))
				} else if scenario == "empty metadata" {
					require.NoError(t, os.WriteFile(filepath.Join(store.volumeDir(req.TargetPath), kataMountInfoFile), []byte("null"), 0600))
				} else if scenario == "nonempty incomplete publication" {
					require.NoError(t, os.WriteFile(filepath.Join(store.volumeDir(req.TargetPath), "sandbox"), nil, 0600))
				} else {
					require.NoError(t, os.WriteFile(filepath.Join(store.volumeDir(req.TargetPath), kataMountInfoFile), nil, 0600))
				}
			}
			target, err := store.FindMountInfo(req.VolumeId)
			// Missing records and JSON null contain no matching volume identity.
			if scenario == "nonempty incomplete publication" || scenario == "empty metadata" {
				require.NoError(t, err)
				assert.Empty(t, target)
			} else {
				require.Error(t, err)
				assert.Empty(t, target)
			}
		})
	}
}

// TestKataForeignVolumes verifies coexistence with other Kata volume types.
func TestKataForeignVolumes(t *testing.T) {
	for _, volumeType := range []string{"blk", "spdkvol", "vfiovol", "spoolvol"} {
		t.Run(volumeType, func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestRequest(t)
			foreignTarget := filepath.Join(t.TempDir(), "foreign")
			foreign := directvolume.MountInfo{
				VolumeType: volumeType, Device: "/dev/foreign", FsType: "ext4",
				Metadata: map[string]string{"fsGroup": "1000"},
			}
			require.NoError(t, d.kataDirectVolume.AddMountInfo(foreignTarget, foreign))
			require.NoError(t, mounts.Mount("/dev/sdd", req.StagingTargetPath, "ext4", []string{"discard"}))
			_, err := d.NodeStageVolume(context.Background(), kataTestStageRequest(req))
			require.NoError(t, err)
			_, err = d.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			_, err = d.NodeStageVolume(context.Background(), kataTestStageRequest(req))
			require.NoError(t, err, "foreign metadata must not interfere with the active-DAV guard")
			_, err = d.NodeExpandVolume(context.Background(), &csi.NodeExpandVolumeRequest{
				VolumeId: req.VolumeId, VolumePath: req.StagingTargetPath,
			})
			assert.Equal(t, codes.Unimplemented, status.Code(err))
			_, err = d.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{
				VolumeId: req.VolumeId, TargetPath: req.TargetPath,
			})
			require.NoError(t, err)
			_, err = d.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
				VolumeId: req.VolumeId, StagingTargetPath: req.StagingTargetPath,
			})
			require.NoError(t, err)

			assert.Zero(t, exec.CommandCalls)
			info, err := d.kataDirectVolume.(*kataTestDirectVolume).VolumeMountInfo(foreignTarget)
			require.NoError(t, err)
			assert.Equal(t, foreign, *info)
		})
	}
}

func TestKataStageAfterPublishAndRestart(t *testing.T) {
	d, mounts, exec := newKataTestDriver(t)
	req := kataTestRequest(t)
	req.Readonly = true
	req.VolumeContext[consts.VolumeAttributePartition] = "1"
	require.NoError(t, mounts.Mount("/dev/sdd-part1", req.StagingTargetPath, "ext4", []string{"discard"}))
	_, err := d.NodeStageVolume(context.Background(), kataTestStageRequest(req))
	require.NoError(t, err)
	_, err = d.NodePublishVolume(context.Background(), req)
	require.NoError(t, err)
	require.NoDirExists(t, req.StagingTargetPath)
	assert.Empty(t, mounts.MountPoints)
	assert.Zero(t, exec.CommandCalls)

	restarted, restartedMounts, restartedExec := newKataTestDriver(t)
	restarted.kataDirectVolume = &kataTestDirectVolume{rootPath: d.kataDirectVolume.(*kataTestDirectVolume).rootPath}
	_, err = restarted.NodeStageVolume(context.Background(), kataTestStageRequest(req))
	require.NoError(t, err)
	assert.Empty(t, restartedMounts.GetLog())
	assert.Zero(t, restartedExec.CommandCalls, "no host filesystem probes, repair or resize after restart")
	assert.NoDirExists(t, req.StagingTargetPath)

	_, err = restarted.NodeExpandVolume(context.Background(), &csi.NodeExpandVolumeRequest{
		VolumeId: req.VolumeId, VolumePath: req.StagingTargetPath, CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
	})
	assert.Equal(t, codes.Unimplemented, status.Code(err))
	_, err = restarted.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{VolumeId: req.VolumeId, StagingTargetPath: req.StagingTargetPath})
	require.NoError(t, err, "unstage relies on CSI unpublish-before-unstage ordering")
	assert.Zero(t, restartedExec.CommandCalls)
}

func TestKataStageAssignmentSafety(t *testing.T) {
	for _, change := range []string{"filesystem", "duplicate"} {
		t.Run(change, func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestRequest(t)
			info := kataTestInfo(t, req)
			if change == "duplicate" {
				require.NoError(t, d.kataDirectVolume.AddMountInfo(req.TargetPath+"-other", info))
			}
			require.NoError(t, d.kataDirectVolume.AddMountInfo(req.TargetPath, info))
			stage := proto.Clone(kataTestStageRequest(req)).(*csi.NodeStageVolumeRequest)
			if change == "filesystem" {
				stage.VolumeCapability.GetMount().FsType = "xfs"
			}
			_, err := d.NodeStageVolume(context.Background(), stage)
			require.NoError(t, err)
			assert.Empty(t, mounts.GetLog())
			assert.Zero(t, exec.CommandCalls)
		})
	}
}

func TestKataStageGuardScope(t *testing.T) {
	for _, scenario := range []string{"enabled filesystem", "disabled filesystem", "native block"} {
		t.Run(scenario, func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestStageRequest(kataTestRequest(t))
			lookups := 0
			d.kataDirectVolume = &kataStubDirectVolume{findMountInfo: func(volumeID string) (string, error) {
				assert.Equal(t, req.VolumeId, volumeID)
				lookups++
				return "", errors.New("inventory unavailable")
			}}
			if scenario == "disabled filesystem" {
				d.enableKataMount = false
			}
			if scenario == "native block" {
				req.VolumeCapability.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
			} else {
				require.NoError(t, mounts.Mount("/dev/sdd", req.StagingTargetPath, "ext4", nil))
			}
			_, err := d.NodeStageVolume(context.Background(), req)
			if scenario == "enabled filesystem" {
				assert.Equal(t, codes.Internal, status.Code(err))
				assert.Equal(t, 1, lookups)
			} else {
				require.NoError(t, err)
				assert.Zero(t, lookups)
			}
			assert.Zero(t, exec.CommandCalls)
		})
	}
}

func TestKataHandoffSerializesLifecycle(t *testing.T) {
	d, _, exec := newKataTestDriver(t)
	req := kataTestRequest(t)
	entered := make(chan struct{})
	finish := make(chan struct{})
	result := make(chan error, 1)
	d.kataDirectVolume = &kataStubDirectVolume{addMountInfo: func(string, directvolume.MountInfo) error {
		close(entered)
		<-finish
		return nil
	}}
	go func() {
		_, err := d.NodePublishVolume(context.Background(), req)
		result <- err
	}()
	defer close(finish)
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("publish ended before handoff: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("publish did not reach handoff")
	}
	_, err := d.NodeStageVolume(context.Background(), kataTestStageRequest(req))
	assert.Equal(t, codes.Aborted, status.Code(err))
	_, err = d.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath})
	assert.Equal(t, codes.Aborted, status.Code(err))
	_, err = d.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{VolumeId: req.VolumeId, StagingTargetPath: req.StagingTargetPath})
	assert.Equal(t, codes.Aborted, status.Code(err))
	_, err = d.NodeExpandVolume(context.Background(), &csi.NodeExpandVolumeRequest{VolumeId: req.VolumeId, VolumePath: req.StagingTargetPath})
	assert.Equal(t, codes.Aborted, status.Code(err))
	assert.Zero(t, exec.CommandCalls)
	t.Cleanup(func() { require.NoError(t, <-result) })
}

func TestKataUnpublishFinishesInterruptedRemoval(t *testing.T) {
	d, _, _ := newKataTestDriver(t)
	req := kataTestRequest(t)
	store := d.kataDirectVolume.(*kataTestDirectVolume)
	require.NoError(t, os.Mkdir(store.volumeDir(req.TargetPath), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(store.volumeDir(req.TargetPath), "sandbox"), nil, 0600))
	leftover, err := store.FindMountInfo(req.VolumeId)
	require.NoError(t, err)
	assert.Empty(t, leftover, "a record-less directory is invisible to lookups")
	_, err = d.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath})
	require.NoError(t, err)
	target, err := store.FindMountInfo(req.VolumeId)
	require.NoError(t, err)
	assert.Empty(t, target)
	assert.NoDirExists(t, store.volumeDir(req.TargetPath))
}

// TestKataUnpublishRetriesRemoval verifies cleanup without reparsing released metadata.
func TestKataUnpublishRetriesRemoval(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(strconv.FormatBool(corrupt), func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestRequest(t)
			store := d.kataDirectVolume.(*kataTestDirectVolume)
			require.NoError(t, store.AddMountInfo(req.TargetPath, kataTestInfo(t, req)))
			if corrupt {
				require.NoError(t, os.WriteFile(filepath.Join(store.volumeDir(req.TargetPath), kataMountInfoFile), []byte("{"), 0600))
			}
			require.NoError(t, os.MkdirAll(req.TargetPath, 0755))
			calls := 0
			d.kataDirectVolume = &kataStubDirectVolume{
				isVolumeMounted: func(string) (bool, error) {
					t.Fatal("released metadata must not be parsed during unpublish")
					return false, nil
				},
				remove: func(target string) error {
					calls++
					assert.Equal(t, req.TargetPath, target)
					assert.NoDirExists(t, target, "target cleanup precedes every metadata removal attempt")
					if calls == 1 {
						return errors.New("interrupted removal")
					}
					return store.Remove(target)
				},
			}
			unpublish := &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath}
			_, err := d.NodeUnpublishVolume(context.Background(), unpublish)
			assert.Equal(t, codes.Internal, status.Code(err))
			assert.DirExists(t, store.volumeDir(req.TargetPath))
			_, err = d.NodeUnpublishVolume(context.Background(), unpublish)
			require.NoError(t, err)
			_, err = d.NodeUnpublishVolume(context.Background(), unpublish)
			require.NoError(t, err)
			assert.Equal(t, 3, calls)
			assert.NoDirExists(t, store.volumeDir(req.TargetPath))
			assert.Empty(t, mounts.GetLog())
			assert.Zero(t, exec.CommandCalls)
		})
	}
}

func TestKataNativeBlockExpandSkipsInventory(t *testing.T) {
	d, _, exec := newKataTestDriver(t)
	d.kataDirectVolume = &kataStubDirectVolume{
		findMountInfo: func(string) (string, error) {
			t.Fatal("native block expansion must not scan filesystem DAV metadata")
			return "", errors.New("inventory unavailable")
		},
		isVolumeMounted: func(string) (bool, error) { return false, nil },
	}
	_, err := d.NodeExpandVolume(context.Background(), &csi.NodeExpandVolumeRequest{
		VolumeId: "block", VolumePath: t.TempDir(),
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}},
	})
	require.NoError(t, err)
	assert.Zero(t, exec.CommandCalls)
}

func TestKataPublishRetryPreservesDAV(t *testing.T) {
	for _, scenario := range []string{"lookup failure", "annotation removed", "driver restart"} {
		t.Run(scenario, func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestRequest(t)
			req.Readonly = true
			require.NoError(t, mounts.Mount("/dev/sdd", req.StagingTargetPath, "ext4", []string{"discard"}))
			_, err := d.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			store := d.kataDirectVolume.(*kataTestDirectVolume)
			before, err := os.ReadFile(filepath.Join(store.volumeDir(req.TargetPath), kataMountInfoFile))
			require.NoError(t, err)
			mounts.ResetLog()
			if scenario == "driver restart" {
				d, mounts, exec = newKataTestDriver(t)
				d.kataDirectVolume = &kataTestDirectVolume{rootPath: store.rootPath}
			}
			client := d.kubeClient.(*fake.Clientset)
			if scenario == "annotation removed" {
				class, err := client.NodeV1().RuntimeClasses().Get(context.Background(), "kata", metav1.GetOptions{})
				require.NoError(t, err)
				class.Annotations = nil
				_, err = client.NodeV1().RuntimeClasses().Update(context.Background(), class, metav1.UpdateOptions{})
				require.NoError(t, err)
			} else {
				client.PrependReactor("*", "*", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
					return true, nil, errors.New("API unavailable")
				})
			}
			client.ClearActions()
			_, err = d.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			assert.Empty(t, client.Actions())
			assert.Empty(t, mounts.GetLog())
			assert.Zero(t, exec.CommandCalls)
			after, err := os.ReadFile(filepath.Join(store.volumeDir(req.TargetPath), kataMountInfoFile))
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.NoDirExists(t, req.StagingTargetPath)
		})
	}
}

func TestKataPublishRetryPreservesOrdinary(t *testing.T) {
	for _, withFSGroup := range []bool{false, true} {
		t.Run(map[bool]string{false: "without fsGroup", true: "with fsGroup"}[withFSGroup], func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestRequest(t)
			client := d.kubeClient.(*fake.Clientset)
			if withFSGroup {
				pod, err := client.CoreV1().Pods("default").Get(context.Background(), "pod", metav1.GetOptions{})
				require.NoError(t, err)
				pod.Spec.SecurityContext = &corev1.PodSecurityContext{FSGroup: ptr.To(int64(3000))}
				_, err = client.CoreV1().Pods("default").Update(context.Background(), pod, metav1.UpdateOptions{})
				require.NoError(t, err)
			}
			failLookup := true
			client.PrependReactor("get", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
				if failLookup {
					return true, nil, errors.New("transient Pod lookup failure")
				}
				return false, nil, nil
			})
			require.NoError(t, mounts.Mount("/dev/sdd", req.StagingTargetPath, "ext4", []string{"discard"}))
			_, err := d.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			failLookup = false
			client.ClearActions()
			mounts.ResetLog()
			_, err = d.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			assert.Empty(t, client.Actions(), "lost reply must not turn an ordinary publication into DAV")
			assert.Empty(t, mounts.GetLog())
			assert.Zero(t, exec.CommandCalls)
			assert.DirExists(t, req.StagingTargetPath)
			target, err := d.kataDirectVolume.FindMountInfo(req.VolumeId)
			require.NoError(t, err)
			assert.Empty(t, target)
		})
	}
}

// TestKataPublishPreservesDamagedTarget keeps ordinary mode without destructive repair.
func TestKataPublishPreservesDamagedTarget(t *testing.T) {
	for _, failure := range []string{"inspection", "readability not probed"} {
		t.Run(failure, func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestRequest(t)
			client := d.kubeClient.(*fake.Clientset)
			failLookup := true
			client.PrependReactor("get", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
				if failLookup {
					return true, nil, errors.New("transient API failure")
				}
				return false, nil, nil
			})
			require.NoError(t, mounts.Mount("/dev/sdd", req.StagingTargetPath, "ext4", []string{"discard"}))
			_, err := d.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			failLookup = false
			if failure == "inspection" {
				mounts.MountCheckErrors = map[string]error{req.TargetPath: errors.New("mount inspection failed")}
			} else {
				// A fake mount entry is enough for an ordinary RW retry; no ReadDir probe.
				require.NoError(t, os.Remove(req.TargetPath))
				require.NoError(t, os.WriteFile(req.TargetPath, nil, 0600))
			}
			mounts.ResetLog()
			for attempt := 0; attempt < 2; attempt++ {
				client.ClearActions()
				_, err = d.NodePublishVolume(context.Background(), req)
				if failure == "inspection" {
					assert.Equal(t, codes.Internal, status.Code(err))
				} else {
					require.NoError(t, err)
				}
				assert.Empty(t, client.Actions())
				assert.Empty(t, mounts.GetLog())
				assert.Len(t, mounts.MountPoints, 2, "failed inspection must not remove mode evidence")
				restarted, _, _ := newKataTestDriver(t)
				restarted.mounter = d.mounter
				restarted.kataDirectVolume = &kataTestDirectVolume{rootPath: d.kataDirectVolume.(*kataTestDirectVolume).rootPath}
				d, client = restarted, restarted.kubeClient.(*fake.Clientset)
			}
			if failure == "inspection" {
				delete(mounts.MountCheckErrors, req.TargetPath)
			} else {
				require.NoError(t, os.Remove(req.TargetPath))
				require.NoError(t, os.Mkdir(req.TargetPath, 0755))
			}
			_, err = d.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			assert.Empty(t, client.Actions())
			assert.Empty(t, mounts.GetLog())
			assert.Zero(t, exec.CommandCalls)
			target, err := d.kataDirectVolume.FindMountInfo(req.VolumeId)
			require.NoError(t, err)
			assert.Empty(t, target, "recovery must not change an ordinary publication into DAV")
		})
	}
}

func TestKataPublishComparesMatchedTarget(t *testing.T) {
	for _, scenario := range []string{"other target", "foreign type"} {
		t.Run(scenario, func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestRequest(t)
			info := kataTestInfo(t, req)
			if scenario == "foreign type" {
				info.VolumeType = "blk"
			}
			require.NoError(t, d.kataDirectVolume.AddMountInfo(req.TargetPath, info))
			store := d.kataDirectVolume.(*kataTestDirectVolume)
			path := filepath.Join(store.volumeDir(req.TargetPath), kataMountInfoFile)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			want := codes.FailedPrecondition
			switch scenario {
			case "foreign type":
				want = codes.OK
			case "other target":
				req.TargetPath += "-other"
			}
			mounts.ResetLog()
			_, err = d.NodePublishVolume(context.Background(), req)
			assert.Equal(t, want, status.Code(err))
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after, "matched publications must not be overwritten")
			assert.Empty(t, d.kubeClient.(*fake.Clientset).Actions())
			assert.Empty(t, mounts.GetLog())
			assert.Zero(t, exec.CommandCalls)
		})
	}
}

// TestKataPublishDetectsOrdinaryMount checks supported CSI mount-point retry behavior.
func TestKataPublishDetectsOrdinaryMount(t *testing.T) {
	for _, scenario := range []string{"mounted", "inspection failure"} {
		t.Run(scenario, func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestRequest(t)
			require.NoError(t, mounts.Mount("/dev/sdd", req.StagingTargetPath, "ext4", []string{"discard"}))
			require.NoError(t, os.MkdirAll(req.TargetPath, 0755))
			require.NoError(t, mounts.Mount("/dev/sdd", req.TargetPath, "ext4", []string{"discard"}))
			if scenario == "inspection failure" {
				mounts.MountCheckErrors = map[string]error{req.TargetPath: errors.New("cannot inspect mount point")}
			}
			mounts.ResetLog()
			_, err := d.NodePublishVolume(context.Background(), req)
			if scenario == "mounted" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.Empty(t, d.kubeClient.(*fake.Clientset).Actions())
			assert.Empty(t, mounts.GetLog())
			assert.Zero(t, exec.CommandCalls)
		})
	}
}

func TestKataPublishCommitBoundaries(t *testing.T) {
	for _, scenario := range []string{"unmount failure", "error after metadata visible"} {
		t.Run(scenario, func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestRequest(t)
			require.NoError(t, mounts.Mount("/dev/sdd", req.StagingTargetPath, "ext4", []string{"discard"}))
			store := d.kataDirectVolume
			if scenario == "unmount failure" {
				mounts.UnmountFunc = func(string) error { return errors.New("unmount failed") }
			} else {
				d.kataDirectVolume = &kataStubDirectVolume{
					findMountInfo: store.FindMountInfo,
					addMountInfo: func(path string, info directvolume.MountInfo) error {
						require.NoError(t, store.AddMountInfo(path, info))
						return errors.New("sync or response failure after publication")
					},
				}
			}
			_, err := d.NodePublishVolume(context.Background(), req)
			require.Error(t, err)
			target, err := store.FindMountInfo(req.VolumeId)
			require.NoError(t, err)
			if scenario == "unmount failure" {
				assert.Empty(t, target)
				assert.DirExists(t, req.StagingTargetPath)
			} else {
				require.Equal(t, req.TargetPath, target)
				mounts.ResetLog()
				_, err = d.NodeStageVolume(context.Background(), kataTestStageRequest(req))
				require.NoError(t, err)
				_, err = d.NodePublishVolume(context.Background(), req)
				require.NoError(t, err)
				assert.Empty(t, mounts.GetLog(), "a visible publication must not be rolled back into a host mount")
				assert.NoDirExists(t, req.StagingTargetPath)
			}
			assert.Zero(t, exec.CommandCalls)
		})
	}
}

func TestKataPublishRetryWithSymlinkedPaths(t *testing.T) {
	d, mounts, exec := newKataTestDriver(t)
	req := kataTestRequest(t)
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	alias := filepath.Join(parent, "alias")
	require.NoError(t, os.MkdirAll(filepath.Join(real, "stage"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(real, "target"), 0755))
	require.NoError(t, os.Symlink(real, alias))
	req.StagingTargetPath, req.TargetPath = filepath.Join(alias, "stage"), filepath.Join(alias, "target")
	require.NoError(t, mounts.Mount("/dev/sdd", req.StagingTargetPath, "ext4", []string{"discard"}))
	require.NoError(t, mounts.Mount("/dev/sdd", req.TargetPath, "ext4", []string{"discard"}))
	mounts.ResetLog()
	_, err := d.NodePublishVolume(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, d.kubeClient.(*fake.Clientset).Actions())
	assert.Empty(t, mounts.GetLog())
	assert.Zero(t, exec.CommandCalls)
}

// TestKataPublishMetadataReadErrors rejects lookup failures before discovery or mounting.
func TestKataPublishMetadataReadErrors(t *testing.T) {
	for _, scenario := range []string{"inventory", "target", "corrupt target file"} {
		t.Run(scenario, func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestRequest(t)
			if scenario == "corrupt target file" {
				store := d.kataDirectVolume.(*kataTestDirectVolume)
				require.NoError(t, store.AddMountInfo(req.TargetPath, kataTestInfo(t, req)))
				require.NoError(t, os.WriteFile(filepath.Join(store.volumeDir(req.TargetPath), kataMountInfoFile), []byte("{"), 0600))
			} else {
				d.kataDirectVolume = &kataStubDirectVolume{
					findMountInfo: func(volumeID string) (string, error) {
						assert.Equal(t, req.VolumeId, volumeID)
						return "", errors.New(scenario + " metadata unavailable")
					},
				}
			}
			_, err := d.NodePublishVolume(context.Background(), req)
			require.Error(t, err)
			assert.Equal(t, codes.Internal, status.Code(err))
			if scenario == "corrupt target file" {
				assert.ErrorContains(t, err, "unusable Kata record")
			}
			assert.Empty(t, d.kubeClient.(*fake.Clientset).Actions())
			assert.Empty(t, mounts.GetLog())
			assert.Zero(t, exec.CommandCalls)
		})
	}
}

// TestKataReadonlyRetryPreservesOrdinary leaves baseline read-only retry repair out of scope.
func TestKataReadonlyRetryPreservesOrdinary(t *testing.T) {
	for _, scenario := range []string{"already readonly", "writable target left unchanged", "symlinked target"} {
		t.Run(scenario, func(t *testing.T) {
			d, mounts, exec := newKataTestDriver(t)
			req := kataTestRequest(t)
			req.Readonly = true
			path := req.TargetPath
			require.NoError(t, os.MkdirAll(path, 0755))
			if scenario == "symlinked target" {
				req.TargetPath = filepath.Join(t.TempDir(), "alias")
				require.NoError(t, os.Symlink(path, req.TargetPath))
			}
			flags := []string{"rw", "nosuid", "nodev", "noexec", "relatime"}
			require.NoError(t, mounts.Mount("/dev/sdd", req.StagingTargetPath, "ext4", flags))
			if scenario == "already readonly" {
				flags[0] = "ro"
			}
			require.NoError(t, mounts.Mount("/dev/sdd", path, "ext4", flags))
			mounts.ResetLog()
			_, err := d.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			_, err = d.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			assert.Zero(t, exec.CommandCalls, "mounted ordinary retries must not verify or repair read-only options")
			require.Len(t, mounts.MountPoints, 2)
			assert.Equal(t, "rw", mounts.MountPoints[0].Opts[0], "staging must remain writable")
			assert.Equal(t, flags, mounts.MountPoints[1].Opts, "target options must remain unchanged")
			assert.Empty(t, mounts.GetLog())
			assert.Empty(t, d.kubeClient.(*fake.Clientset).Actions())
			target, err := d.kataDirectVolume.FindMountInfo(req.VolumeId)
			require.NoError(t, err)
			assert.Empty(t, target)
		})
	}
}
