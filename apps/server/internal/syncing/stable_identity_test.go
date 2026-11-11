package syncing

import "testing"

func TestStableIdentityIgnoresSoftwareVersions(t *testing.T) {
	before := HealthKitSource{BundleID: "com.apple.health.11111111-1111-1111-1111-111111111111", Name: "Watch", Version: "11.1", ProductType: "Watch7,1", OperatingSystemVersion: "11.1.0",
		Device: &HealthKitDevice{Manufacturer: "Apple Inc.", Model: "Watch", HardwareVersion: "Watch7,1", FirmwareVersion: "1", SoftwareVersion: "11.1"}}
	after := before
	after.Name, after.Version, after.OperatingSystemVersion = "Renamed Watch", "11.2", "11.2.0"
	device := *before.Device
	device.FirmwareVersion, device.SoftwareVersion = "2", "11.2"
	after.Device = &device
	a, b := before.StableIdentity(), after.StableIdentity()
	if a.BundleID != b.BundleID || a.ProductType != b.ProductType || *a.Device != *b.Device || a.Version != "" || a.OperatingSystemVersion != "" || a.Name != "" {
		t.Fatalf("stable identity changed across an update: %+v %+v", a, b)
	}
	other := before
	other.ProductType = "Watch9,1"
	if other.StableIdentity().ProductType == a.ProductType {
		t.Fatal("a different device must stay a different identity")
	}
	if (HealthKitSource{BundleID: "x", Device: &HealthKitDevice{SoftwareVersion: "1"}}).StableIdentity().Device != nil {
		t.Fatal("a device with only version fields must carry no device identity")
	}
}

func TestVO2StableEpochIgnoresVersions(t *testing.T) {
	product, model := "Watch7,1", "Watch"
	v1, v2 := "11.1", "11.2"
	a := VO2Provenance{SchemaVersion: "x", SourceRevision: VO2Revision{Source: VO2Source{BundleIdentifier: "com.apple.health.x", Name: "Watch"}, Version: &v1, ProductType: &product, OperatingSystemVersion: VO2OS{11, 1, 0}}, Device: &VO2Device{Model: &model, SoftwareVersion: &v1}}
	b := a
	b.SourceRevision.Version, b.SourceRevision.OperatingSystemVersion = &v2, VO2OS{11, 2, 0}
	b.Device = &VO2Device{Model: &model, SoftwareVersion: &v2}
	if a.EpochID() == b.EpochID() || a.StableEpochID() != b.StableEpochID() {
		t.Fatal("stable VO2 epoch must ignore versions while the full epoch keeps them")
	}
}
