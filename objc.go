package macho

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/blacktop/go-macho/types"
	"github.com/blacktop/go-macho/types/objc"
)

// TODO refactor into a pkg
func (f *File) HasObjC() bool {
	for _, s := range f.Segments() {
		if strings.HasPrefix(s.Name, "__DATA") {
			if sec := f.Section(s.Name, "__objc_imageinfo"); sec != nil {
				return true
			}
		}
	}
	if f.CPU == types.CPU386 {
		if sec := f.Section("__OBJC", "__image_info"); sec != nil {
			return true
		}
	}
	return false
}

func (f *File) HasPlusLoadMethod() bool {
	// TODO add the old way of detecting from dyld3/MachOAnalyzer.cpp
	for _, s := range f.Segments() {
		if strings.HasPrefix(s.Name, "__DATA") {
			if sec := f.Section(s.Name, "__objc_nlclslist"); sec != nil {
				return true
			}
			if sec := f.Section(s.Name, "__objc_nlcatlist"); sec != nil {
				return true
			}
		}
	}
	return false
}

func (f *File) HasObjCMessageReferences() bool {
	for _, s := range f.Segments() {
		if strings.HasPrefix(s.Name, "__DATA") {
			for j := uint32(0); j < s.Nsect; j++ {
				c := f.FileTOC.Sections[j+s.Firstsect]
				if strings.EqualFold("__objc_msgrefs", c.Name) {
					return true
				}
			}
		}
	}
	return false
}

func (f *File) GetObjCInfo() objc.Info {
	var oInfo objc.Info

	for _, sec := range f.FileTOC.Sections {
		if strings.HasPrefix(sec.SectionHeader.Seg, "__DATA") {
			if strings.EqualFold(sec.Name, "__objc_selrefs") {
				oInfo.SelRefCount += sec.SectionHeader.Size / f.pointerSize()
			} else if strings.EqualFold(sec.Name, "__objc_classlist") {
				oInfo.ClassDefCount += sec.SectionHeader.Size / f.pointerSize()
			} else if strings.EqualFold(sec.Name, "__objc_protolist") {
				oInfo.ProtocolDefCount += sec.SectionHeader.Size / f.pointerSize()
			}
		} else if (f.CPU == types.CPU386) && strings.EqualFold(sec.Name, "__OBJC") {
			if strings.EqualFold(sec.Name, "__message_refs") {
				oInfo.SelRefCount += sec.SectionHeader.Size / 4
			} else if strings.EqualFold(sec.Name, "__class") {
				oInfo.ClassDefCount += sec.SectionHeader.Size / 48
			} else if strings.EqualFold(sec.Name, "__protocol") {
				oInfo.ProtocolDefCount += sec.SectionHeader.Size / 20
			}
		}
	}
	return oInfo
}

func (f *File) GetObjCImageInfo() (*objc.ImageInfo, error) {
	var imgInfo objc.ImageInfo
	for _, s := range f.Segments() {
		if strings.HasPrefix(s.Name, "__DATA") {
			if sec := f.Section(s.Name, "__objc_imageinfo"); sec != nil {
				dat, err := sec.Data()
				if err != nil {
					return nil, fmt.Errorf("failed to read __objc_imageinfo: %v", err)
				}

				if err := binary.Read(bytes.NewReader(dat), f.ByteOrder, &imgInfo); err != nil {
					return nil, fmt.Errorf("failed to read ObjCImageInfo: %v", err)
				}

				return &imgInfo, nil
			}
		}
	}
	return nil, fmt.Errorf("file does not contain a __objc_imageinfo section")
}

func (f *File) GetObjCClassInfo(vmAddr uint64) (*objc.ClassRO64, error) {
	var classData objc.ClassRO64

	off, err := f.GetOffset(vmAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to convert vmaddr: %v", err)
	}

	f.sr.Seek(int64(off), io.SeekStart)
	if err := binary.Read(f.sr, f.ByteOrder, &classData); err != nil {
		return nil, fmt.Errorf("failed to read class_ro_t: %v", err)
	}

	return &classData, nil
}

func (f *File) GetObjCMethodNames() (map[string]uint64, error) {
	meth2vmaddr := make(map[string]uint64)

	if sec := f.Section("__TEXT", "__objc_methname"); sec != nil {
		off, err := f.GetOffset(sec.Addr)
		if err != nil {
			return nil, fmt.Errorf("failed to convert vmaddr: %v", err)
		}

		stringPool := make([]byte, sec.Size)

		if _, err := f.sr.ReadAt(stringPool, int64(off)); err != nil {
			return nil, err
		}

		r := bytes.NewBuffer(stringPool[:])

		for {
			s, err := r.ReadString('\x00')
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("failed to read from method name string pool: %v", err)
			}
			meth2vmaddr[strings.Trim(s, "\x00")] = sec.Addr + (sec.Size - uint64(r.Len()+len(s)))
		}
		return meth2vmaddr, nil
	}

	return nil, fmt.Errorf("file does not contain a __TEXT.__objc_methname section")
}

func (f *File) GetObjCClasses() ([]objc.Class, error) {
	var classes []objc.Class

	for _, s := range f.Segments() {
		if strings.HasPrefix(s.Name, "__DATA") {
			if sec := f.Section(s.Name, "__objc_classlist"); sec != nil {
				dat, err := sec.Data()
				if err != nil {
					return nil, fmt.Errorf("failed to read __objc_classlist: %v", err)
				}

				ptrs := make([]uint64, sec.Size/8)
				if err := binary.Read(bytes.NewReader(dat), f.ByteOrder, &ptrs); err != nil {
					return nil, fmt.Errorf("failed to read objc_class_t pointers: %v", err)
				}

				for _, ptr := range ptrs {
					class, err := f.GetObjCClass(f.convertToVMAddr(ptr))
					if err != nil {
						return nil, fmt.Errorf("failed to read objc_class_t at vmaddr: 0x%x; %v", ptr, err)
					}
					classes = append(classes, *class)
				}
				return classes, nil
			}
		}
	}
	return nil, fmt.Errorf("file does not contain a __objc_classlist section")
}

func (f *File) GetObjCPlusLoadClasses() ([]objc.Class, error) {
	var classes []objc.Class

	for _, s := range f.Segments() {
		if strings.HasPrefix(s.Name, "__DATA") {
			if sec := f.Section(s.Name, "__objc_nlclslist"); sec != nil {
				dat, err := sec.Data()
				if err != nil {
					return nil, fmt.Errorf("failed to read __objc_nlclslist: %v", err)
				}

				ptrs := make([]uint64, sec.Size/8)
				if err := binary.Read(bytes.NewReader(dat), f.ByteOrder, &ptrs); err != nil {
					return nil, fmt.Errorf("failed to read objc_class_t pointers: %v", err)
				}

				for _, ptr := range ptrs {
					class, err := f.GetObjCClass(f.convertToVMAddr(ptr))
					if err != nil {
						return nil, fmt.Errorf("failed to read objc_class_t at vmaddr: 0x%x; %v", ptr, err)
					}
					classes = append(classes, *class)
				}
				return classes, nil
			}
		}
	}
	return nil, fmt.Errorf("file does not contain a __objc_nlclslist section")
}

// GetObjCClass parses an ObjC class at a given virtual memory address
func (f *File) GetObjCClass(vmaddr uint64) (*objc.Class, error) {
	var classPtr objc.SwiftClassMetadata64

	off, err := f.GetOffset(vmaddr)
	if err != nil {
		return nil, fmt.Errorf("failed to convert vmaddr: %v", err)
	}

	f.sr.Seek(int64(off), io.SeekStart)
	if err := binary.Read(f.sr, f.ByteOrder, &classPtr); err != nil {
		return nil, fmt.Errorf("failed to read swift_class_metadata_t: %v", err)
	}

	info, err := f.GetObjCClassInfo(f.convertToVMAddr(classPtr.DataVMAddrAndFastFlags) & objc.FAST_DATA_MASK64)
	if err != nil {
		return nil, fmt.Errorf("failed to get class info at vmaddr: 0x%x; %v", classPtr.DataVMAddrAndFastFlags&objc.FAST_DATA_MASK64, err)
	}

	name, err := f.GetCString(f.convertToVMAddr(info.NameVMAddr))
	if err != nil {
		return nil, fmt.Errorf("failed to read cstring: %v", err)
	}

	var methods []objc.Method
	if info.BaseMethodsVMAddr > 0 {
		methods, err = f.GetObjCMethods(f.convertToVMAddr(info.BaseMethodsVMAddr))
		if err != nil {
			return nil, fmt.Errorf("failed to get methods at vmaddr: 0x%x; %v", info.BaseMethodsVMAddr, err)
		}
	}

	var ivars []objc.Ivar
	if info.IvarsVMAddr > 0 {
		ivars, err = f.GetObjCIvars(f.convertToVMAddr(info.IvarsVMAddr))
		if err != nil {
			return nil, fmt.Errorf("failed to get ivars at vmaddr: 0x%x; %v", info.IvarsVMAddr, err)
		}
	}
	var superClass *objc.Class
	if classPtr.SuperclassVMAddr > 0 {
		superClass, err = f.GetObjCClass(f.convertToVMAddr(classPtr.SuperclassVMAddr))
		if err != nil {
			// TODO: these are new chain-fixup binds so I need to link with the chain-fixup imports
			// return nil, fmt.Errorf("failed to read objc_class_t at vmaddr: 0x%x; %v", vmaddr, err)
		}
	}

	return &objc.Class{
		Name:            name,
		SuperClass:      superClass,
		InstanceMethods: methods,
		Ivars:           ivars,
		ClassPtr: types.FilePointer{
			VMAdder: vmaddr,
			Offset:  off,
		},
		IsaVMAddr:             f.convertToVMAddr(classPtr.IsaVMAddr),
		SuperclassVMAddr:      f.convertToVMAddr(classPtr.SuperclassVMAddr),
		MethodCacheBuckets:    classPtr.MethodCacheBuckets,
		MethodCacheProperties: classPtr.MethodCacheProperties,
		DataVMAddr:            f.convertToVMAddr(classPtr.DataVMAddrAndFastFlags) & objc.FAST_DATA_MASK64,
		IsSwiftLegacy:         (classPtr.DataVMAddrAndFastFlags&objc.FAST_IS_SWIFT_LEGACY == 1),
		IsSwiftStable:         (classPtr.DataVMAddrAndFastFlags&objc.FAST_IS_SWIFT_STABLE == 1),
		ReadOnlyData:          *info,
	}, nil
}

func (f *File) GetObjCCategories() ([]objc.Category, error) {
	var categoryPtr objc.CategoryT
	var categories []objc.Category

	for _, s := range f.Segments() {
		if strings.HasPrefix(s.Name, "__DATA") {
			if sec := f.Section(s.Name, "__objc_catlist"); sec != nil {
				dat, err := sec.Data()
				if err != nil {
					return nil, fmt.Errorf("failed to read __objc_catlist: %v", err)
				}

				ptrs := make([]uint64, sec.Size/8)
				if err := binary.Read(bytes.NewReader(dat), f.ByteOrder, &ptrs); err != nil {
					return nil, fmt.Errorf("failed to read objc_category_t pointers: %v", err)
				}

				for _, ptr := range ptrs {
					off, err := f.GetOffset(f.convertToVMAddr(ptr))
					if err != nil {
						return nil, fmt.Errorf("failed to convert vmaddr: %v", err)
					}

					f.sr.Seek(int64(off), io.SeekStart)
					if err := binary.Read(f.sr, f.ByteOrder, &categoryPtr); err != nil {
						return nil, fmt.Errorf("failed to read objc_category_t: %v", err)
					}

					category := objc.Category{CategoryT: categoryPtr}

					category.Name, err = f.GetCString(f.convertToVMAddr(categoryPtr.NameVMAddr))
					if err != nil {
						return nil, fmt.Errorf("failed to read cstring: %v", err)
					}

					categories = append(categories, category)
				}

				return categories, nil
			}
		}
	}

	return nil, fmt.Errorf("file does not contain a __objc_catlist section")
}

func (f *File) GetObjCProtocols() ([]objc.Protocol, error) {
	var protoPtr objc.ProtocolT
	var protocols []objc.Protocol

	for _, s := range f.Segments() {
		if strings.HasPrefix(s.Name, "__DATA") {
			if sec := f.Section(s.Name, "__objc_protolist"); sec != nil {
				dat, err := sec.Data()
				if err != nil {
					return nil, fmt.Errorf("failed to read __objc_protolist: %v", err)
				}

				ptrs := make([]uint64, sec.Size/8)
				if err := binary.Read(bytes.NewReader(dat), f.ByteOrder, &ptrs); err != nil {
					return nil, fmt.Errorf("failed to read protocol_t pointers: %v", err)
				}

				for _, ptr := range ptrs {
					off, err := f.GetOffset(f.convertToVMAddr(ptr))
					if err != nil {
						return nil, fmt.Errorf("failed to convert vmaddr: %v", err)
					}

					f.sr.Seek(int64(off), io.SeekStart)
					if err := binary.Read(f.sr, f.ByteOrder, &protoPtr); err != nil {
						return nil, fmt.Errorf("failed to read protocol_t: %v", err)
					}

					proto := objc.Protocol{ProtocolT: protoPtr}

					proto.Name, err = f.GetCString(f.convertToVMAddr(protoPtr.NameVMAddr))
					if err != nil {
						return nil, fmt.Errorf("failed to read cstring: %v", err)
					}

					if protoPtr.InstanceMethodsVMAddr > 0 {
						proto.InstanceMethods, err = f.GetObjCMethods(f.convertToVMAddr(protoPtr.InstanceMethodsVMAddr))
						if err != nil {
							return nil, err
						}
					}
					if protoPtr.ClassMethodsVMAddr > 0 {
						proto.ClassMethods, err = f.GetObjCMethods(f.convertToVMAddr(protoPtr.ClassMethodsVMAddr))
						if err != nil {
							return nil, err
						}
					}
					if protoPtr.OptionalInstanceMethodsVMAddr > 0 {
						proto.OptionalInstanceMethods, err = f.GetObjCMethods(f.convertToVMAddr(protoPtr.OptionalInstanceMethodsVMAddr))
						if err != nil {
							return nil, err
						}
					}
					if protoPtr.OptionalClassMethodsVMAddr > 0 {
						proto.OptionalClassMethods, err = f.GetObjCMethods(f.convertToVMAddr(protoPtr.OptionalClassMethodsVMAddr))
						if err != nil {
							return nil, err
						}
					}
					if protoPtr.InstancePropertiesVMAddr > 0 {
						proto.InstanceProperties, err = f.GetObjCProperties(f.convertToVMAddr(protoPtr.InstancePropertiesVMAddr))
						if err != nil {
							return nil, err
						}
					}
					if protoPtr.ExtendedMethodTypesVMAddr > 0 {
						extOff, err := f.GetOffset(f.convertToVMAddr(protoPtr.ExtendedMethodTypesVMAddr))
						if err != nil {
							return nil, fmt.Errorf("failed to convert vmaddr: %v", err)
						}

						f.sr.Seek(int64(extOff), io.SeekStart)
						var extMPtr uint64
						if err := binary.Read(f.sr, f.ByteOrder, &extMPtr); err != nil {
							return nil, fmt.Errorf("failed to read ExtendedMethodTypesVMAddr: %v", err)
						}

						proto.ExtendedMethodTypes, err = f.GetCString(f.convertToVMAddr(extMPtr))
						if err != nil {
							return nil, fmt.Errorf("failed to read cstring: %v", err)
						}
					}
					if protoPtr.DemangledNameVMAddr > 0 {
						dnOff, err := f.GetOffset(f.convertToVMAddr(protoPtr.DemangledNameVMAddr))
						if err != nil {
							return nil, fmt.Errorf("failed to convert vmaddr: %v", err)
						}

						f.sr.Seek(int64(dnOff), io.SeekStart)
						var dnPtr uint64
						if err := binary.Read(f.sr, f.ByteOrder, &dnPtr); err != nil {
							return nil, fmt.Errorf("failed to read DemangledNameVMAddr: %v", err)
						}

						proto.DemangledName, err = f.GetCString(f.convertToVMAddr(dnPtr))
						if err != nil {
							return nil, fmt.Errorf("failed to read cstring: %v", err)
						}
					}

					protocols = append(protocols, proto)
				}
				return protocols, nil
			}
		}
	}
	return nil, fmt.Errorf("file does not contain a __objc_protolist section")
}

func (f *File) GetObjCMethodList() ([]objc.Method, error) {
	var methodList objc.MethodList
	var objcMethods []objc.Method

	if sec := f.Section("__TEXT", "__objc_methlist"); sec != nil {
		mlr := io.NewSectionReader(f.sr, int64(sec.Offset), int64(sec.Size))

		for {
			err := binary.Read(mlr, f.ByteOrder, &methodList)

			currOffset, _ := mlr.Seek(0, io.SeekCurrent)
			currOffset += int64(sec.Offset)
			// currOffset += int64(sec.Offset) + int64(binary.Size(objc.MethodList{}))

			if err == io.EOF {
				break
			}

			if err != nil {
				return nil, fmt.Errorf("failed to read method_list_t: %v", err)
			}

			methods := make([]objc.MethodSmallT, methodList.Count)
			if err := binary.Read(mlr, f.ByteOrder, &methods); err != nil {
				return nil, fmt.Errorf("failed to read method_t(s) (small): %v", err)
			}

			for _, method := range methods {
				var nameAddr uint32
				f.sr.Seek(int64(method.NameOffset)+currOffset, io.SeekStart)
				if err := binary.Read(f.sr, f.ByteOrder, &nameAddr); err != nil {
					return nil, fmt.Errorf("failed to read nameAddr(small): %v", err)
				}
				n, err := f.GetCString(uint64(nameAddr))
				if err != nil {
					return nil, fmt.Errorf("failed to read cstring: %v", err)
				}

				typesVMAddr, err := f.GetVMAddress(uint64(method.TypesOffset) + uint64(currOffset+4))
				if err != nil {
					return nil, fmt.Errorf("failed to convert offset 0x%x to vmaddr; %v", method.TypesOffset, err)
				}
				t, err := f.GetCString(typesVMAddr)
				if err != nil {
					return nil, fmt.Errorf("failed to read cstring: %v", err)
				}

				impVMAddr, err := f.GetVMAddress(uint64(method.ImpOffset) + uint64(currOffset+8))
				if err != nil {
					return nil, fmt.Errorf("failed to convert offset 0x%x to vmaddr; %v", method.ImpOffset, err)
				}

				currOffset += int64(methodList.EntSize())

				objcMethods = append(objcMethods, objc.Method{
					NameVMAddr:  uint64(nameAddr),
					TypesVMAddr: typesVMAddr,
					ImpVMAddr:   impVMAddr,
					Name:        n,
					Types:       t,
					Pointer: types.FilePointer{
						VMAdder: impVMAddr,
						Offset:  int64(method.ImpOffset),
					},
				})
			}

			curr, _ := mlr.Seek(0, io.SeekCurrent)
			align := types.RoundUp(uint64(curr), 8)
			mlr.Seek(int64(align), io.SeekStart)
		}

		return objcMethods, nil
	}
	return nil, fmt.Errorf("file does not contain a __objc_methlist section")
}

func (f *File) GetObjCMethods(vmAddr uint64) ([]objc.Method, error) {

	var methodList objc.MethodList

	off, err := f.GetOffset(vmAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to convert vmaddr: %v", err)
	}

	f.sr.Seek(int64(off), io.SeekStart)
	if err := binary.Read(f.sr, f.ByteOrder, &methodList); err != nil {
		return nil, fmt.Errorf("failed to read method_list_t: %v", err)
	}

	if methodList.IsSmall() {
		return f.readSmallMethods(methodList)
	}

	return f.readBigMethods(methodList)
}

func (f *File) readSmallMethods(methodList objc.MethodList) ([]objc.Method, error) {
	var objcMethods []objc.Method

	currOffset, _ := f.sr.Seek(0, io.SeekCurrent)

	methods := make([]objc.MethodSmallT, methodList.Count)
	if err := binary.Read(f.sr, f.ByteOrder, &methods); err != nil {
		return nil, fmt.Errorf("failed to read method_t(s) (small): %v", err)
	}

	for _, method := range methods {
		var nameAddr uint32
		f.sr.Seek(int64(method.NameOffset)+currOffset, io.SeekStart)
		if err := binary.Read(f.sr, f.ByteOrder, &nameAddr); err != nil {
			return nil, fmt.Errorf("failed to read nameAddr(small): %v", err)
		}
		n, err := f.GetCString(uint64(nameAddr))
		if err != nil {
			return nil, fmt.Errorf("failed to read cstring: %v", err)
		}

		typesVMAddr, err := f.GetVMAddress(uint64(method.TypesOffset) + uint64(currOffset+4))
		if err != nil {
			return nil, fmt.Errorf("failed to convert offset 0x%x to vmaddr; %v", method.TypesOffset, err)
		}
		t, err := f.GetCString(typesVMAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to read cstring: %v", err)
		}

		impVMAddr, err := f.GetVMAddress(uint64(method.ImpOffset) + uint64(currOffset+8))
		if err != nil {
			return nil, fmt.Errorf("failed to convert offset 0x%x to vmaddr; %v", method.ImpOffset, err)
		}

		currOffset += int64(methodList.EntSize())

		objcMethods = append(objcMethods, objc.Method{
			NameVMAddr:  uint64(nameAddr),
			TypesVMAddr: typesVMAddr,
			ImpVMAddr:   impVMAddr,
			Name:        n,
			Types:       t,
			Pointer: types.FilePointer{
				VMAdder: impVMAddr,
				Offset:  int64(method.ImpOffset),
			},
		})
	}

	return objcMethods, nil
}

func (f *File) readBigMethods(methodList objc.MethodList) ([]objc.Method, error) {
	var objcMethods []objc.Method

	methods := make([]objc.MethodT, methodList.Count)
	if err := binary.Read(f.sr, f.ByteOrder, &methods); err != nil {
		return nil, fmt.Errorf("failed to read method_t: %v", err)
	}

	for _, method := range methods {
		n, err := f.GetCString(f.convertToVMAddr(uint64(method.NameVMAddr)))
		if err != nil {
			return nil, fmt.Errorf("failed to read cstring: %v", err)
		}
		t, err := f.GetCString(f.convertToVMAddr(uint64(method.TypesVMAddr)))
		if err != nil {
			return nil, fmt.Errorf("failed to read cstring: %v", err)
		}
		impOff, err := f.GetOffset(f.convertToVMAddr(method.ImpVMAddr))
		if err != nil {
			return nil, fmt.Errorf("failed to convert vmaddr: %v", err)
		}
		objcMethods = append(objcMethods, objc.Method{
			NameVMAddr:  method.NameVMAddr,
			TypesVMAddr: method.TypesVMAddr,
			ImpVMAddr:   method.ImpVMAddr,
			Name:        n,
			Types:       t,
			Pointer: types.FilePointer{
				VMAdder: method.ImpVMAddr,
				Offset:  impOff,
			},
		})
	}

	return objcMethods, nil
}

func (f *File) GetObjCIvars(vmAddr uint64) ([]objc.Ivar, error) {

	var ivarsList objc.IvarList
	var ivars []objc.Ivar

	off, err := f.GetOffset(vmAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to convert vmaddr: %v", err)
	}

	f.sr.Seek(int64(off), io.SeekStart)
	if err := binary.Read(f.sr, f.ByteOrder, &ivarsList); err != nil {
		return nil, fmt.Errorf("failed to read objc_ivar_list_t: %v", err)
	}

	ivs := make([]objc.IvarT, ivarsList.Count)
	if err := binary.Read(f.sr, f.ByteOrder, &ivs); err != nil {
		return nil, fmt.Errorf("failed to read objc_ivar_list_t: %v", err)
	}

	for _, ivar := range ivs {
		n, err := f.GetCString(f.convertToVMAddr(uint64(ivar.NameVMAddr)))
		if err != nil {
			return nil, fmt.Errorf("failed to read cstring: %v", err)
		}
		t, err := f.GetCString(f.convertToVMAddr(uint64(ivar.TypesVMAddr)))
		if err != nil {
			return nil, fmt.Errorf("failed to read cstring: %v", err)
		}
		ivars = append(ivars, objc.Ivar{
			Name:  n,
			Type:  t,
			IvarT: ivar,
		})
	}

	return ivars, nil
}

func (f *File) GetObjCProperties(vmAddr uint64) ([]objc.Property, error) {

	var propList objc.PropertyList
	var objcProperties []objc.Property

	off, err := f.GetOffset(vmAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to convert vmaddr: %v", err)
	}

	f.sr.Seek(int64(off), io.SeekStart)
	if err := binary.Read(f.sr, f.ByteOrder, &propList); err != nil {
		return nil, fmt.Errorf("failed to read objc_property_list_t: %v", err)
	}

	properties := make([]objc.PropertyT, propList.Count)
	if err := binary.Read(f.sr, f.ByteOrder, &properties); err != nil {
		return nil, fmt.Errorf("failed to read objc_property_t: %v", err)
	}

	for _, prop := range properties {
		name, err := f.GetCString(f.convertToVMAddr(prop.NameVMAddr))
		if err != nil {
			return nil, fmt.Errorf("failed to read cstring: %v", err)
		}
		attrib, err := f.GetCString(f.convertToVMAddr(prop.AttributesVMAddr))
		if err != nil {
			return nil, fmt.Errorf("failed to read cstring: %v", err)
		}
		objcProperties = append(objcProperties, objc.Property{
			PropertyT:  prop,
			Name:       name,
			Attributes: attrib,
		})
	}

	return objcProperties, nil
}

func (f *File) GetObjCSelectorReferences() (map[uint64]string, error) {
	var selPtrs []uint64
	selRefs := make(map[uint64]string)

	for _, s := range f.Segments() {
		if strings.HasPrefix(s.Name, "__DATA") {
			if sec := f.Section(s.Name, "__objc_selrefs"); sec != nil {
				dat, err := sec.Data()
				if err != nil {
					return nil, fmt.Errorf("failed to read __objc_selrefs: %v", err)
				}

				selPtrs = make([]uint64, sec.Size/8)
				if err := binary.Read(bytes.NewReader(dat), f.ByteOrder, &selPtrs); err != nil {
					return nil, fmt.Errorf("failed to read selector pointers: %v", err)
				}

				for _, sel := range selPtrs {
					selName, err := f.GetCString(f.convertToVMAddr(sel))
					if err != nil {
						return nil, fmt.Errorf("failed to read cstring: %v", err)
					}
					selRefs[f.convertToVMAddr(sel)] = selName
				}
				return selRefs, nil
			}
		}
	}
	return nil, fmt.Errorf("file does not contain a __objc_selrefs section")
}
