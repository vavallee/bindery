import { useNavigate } from 'react-router'
import AddToLibraryModal from '../../components/AddToLibraryModal'

// Search and request, for requesters: the same metadata search the Add
// dialog runs, where the confirm step sends a request instead of adding.
// Closing the dialog goes back to the requester's list.
export default function RequestSearchPage() {
  const navigate = useNavigate()
  return (
    <AddToLibraryModal
      standalone
      onClose={() => navigate('/my-requests')}
      onAdded={() => navigate('/my-requests')}
      onRequested={() => navigate('/my-requests')}
    />
  )
}
