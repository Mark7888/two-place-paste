import { Empty } from "../components/Card";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { Link } from "../router";

export function NotFoundPage() {
  useDocumentTitle("Not found");
  return (
    <>
      <h1>Not found</h1>
      <Empty>
        There is no such screen. <Link to="/admin/">Go to the dashboard</Link>.
      </Empty>
    </>
  );
}
