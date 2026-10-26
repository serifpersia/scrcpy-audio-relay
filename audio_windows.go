//go:build windows

package main

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

type policyConfig struct{ ole.IUnknown }

type policyConfigVtbl struct {
	ole.IUnknownVtbl
	getMixFormat          uintptr
	getDeviceFormat       uintptr
	resetDeviceFormat     uintptr
	setDeviceFormat       uintptr
	getProcessingPeriod   uintptr
	setProcessingPeriod   uintptr
	getShareMode          uintptr
	setShareMode          uintptr
	getPropertyValue      uintptr
	setPropertyValue      uintptr
	setDefaultEndpoint    uintptr
	setEndpointVisibility uintptr
}

func (v *policyConfig) vtable() *policyConfigVtbl {
	return (*policyConfigVtbl)(unsafe.Pointer(v.RawVTable))
}

func (v *policyConfig) setDefault(deviceID string, role uint32) error {
	id, err := syscall.UTF16PtrFromString(deviceID)
	if err != nil {
		return err
	}
	hr, _, _ := syscall.Syscall(v.vtable().setDefaultEndpoint, 3, uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(id)), uintptr(role))
	if hr != 0 {
		return ole.NewError(hr)
	}
	return nil
}

var (
	clsidPolicyConfig = ole.NewGUID("{870af99c-171d-4f9e-af0d-e63df40c2bc9}")
	iidPolicyConfig   = ole.NewGUID("{f8679f50-850a-41cf-9c72-430f290290c8}")
)

func withCOM(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		return err
	}
	defer ole.CoUninitialize()
	return fn()
}

func deviceEnumerator() (*wca.IMMDeviceEnumerator, error) {
	var mmde *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &mmde); err != nil {
		return nil, err
	}
	return mmde, nil
}

func deviceInfo(mmd *wca.IMMDevice) (string, string, error) {
	var id string
	if err := mmd.GetId(&id); err != nil {
		return "", "", err
	}
	var ps *wca.IPropertyStore
	if err := mmd.OpenPropertyStore(wca.STGM_READ, &ps); err != nil {
		return "", "", err
	}
	defer ps.Release()
	var pv wca.PROPVARIANT
	if err := ps.GetValue(&wca.PKEY_Device_FriendlyName, &pv); err != nil {
		return "", "", err
	}
	defer pv.Clear()
	return id, pv.String(), nil
}

func findDevice(mmde *wca.IMMDeviceEnumerator, id string) (*wca.IMMDevice, error) {
	if id == "" || id == "default" {
		var mmd *wca.IMMDevice
		err := mmde.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &mmd)
		return mmd, err
	}
	var col *wca.IMMDeviceCollection
	if err := mmde.EnumAudioEndpoints(wca.ERender, wca.DEVICE_STATE_ACTIVE, &col); err != nil {
		return nil, err
	}
	defer col.Release()
	var count uint32
	if err := col.GetCount(&count); err != nil {
		return nil, err
	}
	for i := uint32(0); i < count; i++ {
		var mmd *wca.IMMDevice
		if err := col.Item(i, &mmd); err != nil {
			continue
		}
		var got string
		if err := mmd.GetId(&got); err == nil && got == id {
			return mmd, nil
		}
		mmd.Release()
	}
	return nil, errors.New("audio device not found")
}

func endpointVolume(mmd *wca.IMMDevice) (*wca.IAudioEndpointVolume, error) {
	var aev *wca.IAudioEndpointVolume
	err := mmd.Activate(wca.IID_IAudioEndpointVolume, wca.CLSCTX_ALL, nil, &aev)
	return aev, err
}

func audioDevices() []audioDevice {
	result := []audioDevice{}
	withCOM(func() error {
		mmde, err := deviceEnumerator()
		if err != nil {
			return err
		}
		defer mmde.Release()
		var col *wca.IMMDeviceCollection
		if err := mmde.EnumAudioEndpoints(wca.ERender, wca.DEVICE_STATE_ACTIVE, &col); err != nil {
			return err
		}
		defer col.Release()
		var count uint32
		if err := col.GetCount(&count); err != nil {
			return err
		}
		for i := uint32(0); i < count; i++ {
			var mmd *wca.IMMDevice
			if err := col.Item(i, &mmd); err != nil {
				continue
			}
			id, label, err := deviceInfo(mmd)
			mmd.Release()
			if err == nil {
				result = append(result, audioDevice{ID: id, Label: label})
			}
		}
		return nil
	})
	return result
}

func audioControls(device string) []audioControl {
	result := []audioControl{}
	err := withCOM(func() error {
		mmde, err := deviceEnumerator()
		if err != nil {
			return err
		}
		defer mmde.Release()
		mmd, err := findDevice(mmde, device)
		if err != nil {
			return err
		}
		defer mmd.Release()
		aev, err := endpointVolume(mmd)
		if err != nil {
			return err
		}
		defer aev.Release()
		var level float32
		if err := aev.GetMasterVolumeLevelScalar(&level); err != nil {
			return err
		}
		var muted bool
		if err := aev.GetMute(&muted); err != nil {
			return err
		}
		result = append(result, audioControl{Name: "Master", Volume: int(level*100 + 0.5), Muted: muted})
		return nil
	})
	if err != nil {
		return []audioControl{}
	}
	return result
}

func setAudioVolume(device, control string, volume int) error {
	if control != "Master" {
		return errors.New("unsupported mixer control")
	}
	return withCOM(func() error {
		mmde, err := deviceEnumerator()
		if err != nil {
			return err
		}
		defer mmde.Release()
		mmd, err := findDevice(mmde, device)
		if err != nil {
			return err
		}
		defer mmd.Release()
		aev, err := endpointVolume(mmd)
		if err != nil {
			return err
		}
		defer aev.Release()
		if err := aev.SetMasterVolumeLevelScalar(float32(volume)/100, nil); err != nil {
			return err
		}
		aev.SetMute(false, nil)
		return nil
	})
}

func applyAudioDevice(device string) error {
	if device == "" || device == "default" {
		return nil
	}
	return withCOM(func() error {
		var pc *policyConfig
		if err := wca.CoCreateInstance(clsidPolicyConfig, 0, wca.CLSCTX_ALL, iidPolicyConfig, &pc); err != nil {
			return err
		}
		defer pc.Release()
		for _, role := range []uint32{wca.EConsole, wca.EMultimedia, wca.ECommunications} {
			if err := pc.setDefault(device, role); err != nil {
				return err
			}
		}
		return nil
	})
}

func audioEnv(device string) []string { return nil }
