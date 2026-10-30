// IMFActivate delegates its attribute store to MFCreateAttributes.
HRESULT STDMETHODCALLTYPE GetItem(REFGUID guidKey, PROPVARIANT *pValue) override {
    return attributes->GetItem(guidKey, pValue);
}
HRESULT STDMETHODCALLTYPE GetItemType(REFGUID guidKey, MF_ATTRIBUTE_TYPE *pType) override {
    return attributes->GetItemType(guidKey, pType);
}
HRESULT STDMETHODCALLTYPE CompareItem(REFGUID guidKey, REFPROPVARIANT Value,
                                      WINBOOL *pbResult) override {
    return attributes->CompareItem(guidKey, Value, pbResult);
}
HRESULT STDMETHODCALLTYPE Compare(IMFAttributes *pTheirs, MF_ATTRIBUTES_MATCH_TYPE MatchType,
                                  WINBOOL *pbResult) override {
    return attributes->Compare(pTheirs, MatchType, pbResult);
}
HRESULT STDMETHODCALLTYPE GetUINT32(REFGUID guidKey, UINT32 *punValue) override {
    return attributes->GetUINT32(guidKey, punValue);
}
HRESULT STDMETHODCALLTYPE GetUINT64(REFGUID guidKey, UINT64 *punValue) override {
    return attributes->GetUINT64(guidKey, punValue);
}
HRESULT STDMETHODCALLTYPE GetDouble(REFGUID guidKey, double *pfValue) override {
    return attributes->GetDouble(guidKey, pfValue);
}
HRESULT STDMETHODCALLTYPE GetGUID(REFGUID guidKey, GUID *pguidValue) override {
    return attributes->GetGUID(guidKey, pguidValue);
}
HRESULT STDMETHODCALLTYPE GetStringLength(REFGUID guidKey, UINT32 *pcchLength) override {
    return attributes->GetStringLength(guidKey, pcchLength);
}
HRESULT STDMETHODCALLTYPE GetString(REFGUID guidKey, LPWSTR pwszValue, UINT32 cchBufSize,
                                    UINT32 *pcchLength) override {
    return attributes->GetString(guidKey, pwszValue, cchBufSize, pcchLength);
}
HRESULT STDMETHODCALLTYPE GetAllocatedString(REFGUID guidKey, LPWSTR *ppwszValue,
                                             UINT32 *pcchLength) override {
    return attributes->GetAllocatedString(guidKey, ppwszValue, pcchLength);
}
HRESULT STDMETHODCALLTYPE GetBlobSize(REFGUID guidKey, UINT32 *pcbBlobSize) override {
    return attributes->GetBlobSize(guidKey, pcbBlobSize);
}
HRESULT STDMETHODCALLTYPE GetBlob(REFGUID guidKey, UINT8 *pBuf, UINT32 cbBufSize,
                                  UINT32 *pcbBlobSize) override {
    return attributes->GetBlob(guidKey, pBuf, cbBufSize, pcbBlobSize);
}
HRESULT STDMETHODCALLTYPE GetAllocatedBlob(REFGUID guidKey, UINT8 **ppBuf,
                                           UINT32 *pcbSize) override {
    return attributes->GetAllocatedBlob(guidKey, ppBuf, pcbSize);
}
HRESULT STDMETHODCALLTYPE GetUnknown(REFGUID guidKey, REFIID riid, LPVOID *ppv) override {
    return attributes->GetUnknown(guidKey, riid, ppv);
}
HRESULT STDMETHODCALLTYPE SetItem(REFGUID guidKey, REFPROPVARIANT Value) override {
    return attributes->SetItem(guidKey, Value);
}
HRESULT STDMETHODCALLTYPE DeleteItem(REFGUID guidKey) override {
    return attributes->DeleteItem(guidKey);
}
HRESULT STDMETHODCALLTYPE DeleteAllItems() override { return attributes->DeleteAllItems(); }
HRESULT STDMETHODCALLTYPE SetUINT32(REFGUID guidKey, UINT32 unValue) override {
    return attributes->SetUINT32(guidKey, unValue);
}
HRESULT STDMETHODCALLTYPE SetUINT64(REFGUID guidKey, UINT64 unValue) override {
    return attributes->SetUINT64(guidKey, unValue);
}
HRESULT STDMETHODCALLTYPE SetDouble(REFGUID guidKey, double fValue) override {
    return attributes->SetDouble(guidKey, fValue);
}
HRESULT STDMETHODCALLTYPE SetGUID(REFGUID guidKey, REFGUID guidValue) override {
    return attributes->SetGUID(guidKey, guidValue);
}
HRESULT STDMETHODCALLTYPE SetString(REFGUID guidKey, LPCWSTR wszValue) override {
    return attributes->SetString(guidKey, wszValue);
}
HRESULT STDMETHODCALLTYPE SetBlob(REFGUID guidKey, const UINT8 *pBuf, UINT32 cbBufSize) override {
    return attributes->SetBlob(guidKey, pBuf, cbBufSize);
}
HRESULT STDMETHODCALLTYPE SetUnknown(REFGUID guidKey, IUnknown *pUnknown) override {
    return attributes->SetUnknown(guidKey, pUnknown);
}
HRESULT STDMETHODCALLTYPE LockStore() override { return attributes->LockStore(); }
HRESULT STDMETHODCALLTYPE UnlockStore() override { return attributes->UnlockStore(); }
HRESULT STDMETHODCALLTYPE GetCount(UINT32 *pcItems) override {
    return attributes->GetCount(pcItems);
}
HRESULT STDMETHODCALLTYPE GetItemByIndex(UINT32 unIndex, GUID *pguidKey,
                                         PROPVARIANT *pValue) override {
    return attributes->GetItemByIndex(unIndex, pguidKey, pValue);
}
HRESULT STDMETHODCALLTYPE CopyAllItems(IMFAttributes *pDest) override {
    return attributes->CopyAllItems(pDest);
}
